package redisstorage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/goforj/storage/storagecore"
	"github.com/redis/go-redis/v9"
)

// deleteObjectContext makes source validation and index removal one optimistic transaction.
func (d *driver) deleteObjectContext(ctx context.Context, key string) error {
	return d.deleteObjectRevision(ctx, key, "")
}

// deleteObjectRevision optionally validates the copied revision within the source WATCH.
func (d *driver) deleteObjectRevision(ctx context.Context, key, revision string) error {
	return d.watchTransaction(ctx, []string{d.objectKey(key)}, func(tx *redis.Tx) error {
		exists, err := tx.HExists(ctx, d.objectKey(key), "data").Result()
		if err != nil {
			return err
		}
		if !exists {
			if revision != "" {
				return storagecore.ErrNotFound
			}
			return errDeleteRedispatch
		}
		if revision != "" {
			_, current, err := d.moveFileRevision(ctx, tx, key)
			if err != nil {
				return err
			}
			if current != revision {
				return storagecore.ErrConflict
			}
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			d.queueUnindexDelete(ctx, pipe, key)
			d.queuePruneEmptyDirs(ctx, pipe, objectDirs(key))
			return nil
		})
		return err
	})
}

// deleteDirectoryContext commits only while the directory remains empty across Redis clients.
func (d *driver) deleteDirectoryContext(ctx context.Context, key string) error {
	watchKeys := []string{
		d.objectKey(key),
		d.dirObjectsKey(key),
		d.dirChildrenKey(key),
		d.versionedDirObjectsKey(key),
		d.versionedDirChildrenKey(key),
	}
	return d.watchTransaction(ctx, watchKeys, func(tx *redis.Tx) error {
		isObject, err := tx.HExists(ctx, d.objectKey(key), "data").Result()
		if err != nil {
			return err
		}
		if isObject {
			return errDeleteRedispatch
		}
		exists, hasContents, err := d.directoryStateInTransaction(ctx, tx, key)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
		}
		if hasContents {
			return fmt.Errorf("%w: directory not empty", storagecore.ErrForbidden)
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, d.dirObjectsKey(key))
			pipe.Del(ctx, d.dirChildrenKey(key))
			pipe.Del(ctx, d.versionedDirObjectsKey(key))
			pipe.Del(ctx, d.versionedDirChildrenKey(key))
			pipe.SRem(ctx, d.versionedDirChildrenKey(parentDir(key)), encodeDirChild(key))
			pipe.SRem(ctx, d.dirChildrenKey(parentDir(key)), encodeDirChild(key))
			markers := []any{dirMarkerMember(key), legacyDirMarkerMember(key)}
			pipe.SRem(ctx, d.versionedDirObjectsKey(""), markers...)
			for _, dir := range objectDirs(key) {
				pipe.SRem(ctx, d.versionedDirObjectsKey(dir), markers...)
			}
			d.queuePruneEmptyDirs(ctx, pipe, objectDirs(key))
			return nil
		})
		return err
	})
}

// ensureObjectPath rejects file-directory collisions before metadata is
// changed, matching filesystems where a path cannot represent both kinds.
func (d *driver) ensureObjectPath(ctx context.Context, tx *redis.Tx, key string) error {
	directory, _, err := d.directoryStateInTransaction(ctx, tx, key)
	if err != nil {
		return err
	}
	if directory {
		return fmt.Errorf("%w: destination is a directory", storagecore.ErrForbidden)
	}
	for _, parent := range objectDirs(key) {
		exists, err := tx.HExists(ctx, d.objectKey(parent), "data").Result()
		if err != nil {
			return fmt.Errorf("storage: redis stat: %w", err)
		}
		if exists {
			return fmt.Errorf("%w: parent path %q is an object", storagecore.ErrForbidden, d.stripPrefix(parent))
		}
	}
	return nil
}

// ensureDirectoryPath rejects directories that would overlap an object while
// retaining idempotent MakeDir behavior for existing directories.
func (d *driver) ensureDirectoryPath(ctx context.Context, client redis.Cmdable, key string) error {
	for _, candidate := range append(objectDirs(key), key) {
		exists, err := client.HExists(ctx, d.objectKey(candidate), "data").Result()
		if err != nil {
			return fmt.Errorf("storage: redis stat: %w", err)
		}
		if exists {
			return fmt.Errorf("%w: path %q is an object", storagecore.ErrForbidden, d.stripPrefix(candidate))
		}
	}
	return nil
}

// objectCollisionKeys contains every key whose change can turn an object path
// into a file-directory collision while a WATCH transaction is in flight.
func (d *driver) objectCollisionKeys(key string) []string {
	keys := []string{d.objectKey(key), d.dirObjectsKey(key), d.versionedDirObjectsKey(key)}
	for _, parent := range objectDirs(key) {
		keys = append(keys, d.objectKey(parent))
	}
	return keys
}

// directoryCollisionKeys contains object hashes that must remain absent while
// MakeDir records its directory indexes.
func (d *driver) directoryCollisionKeys(key string) []string {
	candidates := append(objectDirs(key), key)
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, d.objectKey(candidate))
	}
	return keys
}

// watchTransaction retries optimistic transactions until they commit or the
// caller's context ends, making collision checks atomic across Redis clients.
func (d *driver) watchTransaction(ctx context.Context, keys []string, fn func(*redis.Tx) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := d.client.Watch(ctx, fn, keys...)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return err
	}
}

// DeleteMoveDirectoryContext prevents move cleanup from redispatching to a newly created file.
func (d *driver) DeleteMoveDirectoryContext(ctx context.Context, name string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	key, err := d.key(name)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" {
		return fmt.Errorf("%w: logical root cannot be deleted", storagecore.ErrForbidden)
	}
	err = d.deleteDirectoryContext(ctx, key)
	if errors.Is(err, errDeleteRedispatch) {
		return storagecore.ErrConflict
	}
	return err
}

// CopyMoveFileContext records the source revision together with its payload before writing the destination.
func (d *driver) CopyMoveFileContext(ctx context.Context, src, dst string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key, err := d.key(src)
	if err != nil {
		return "", err
	}
	if d.stripPrefix(key) == "" {
		return "", fmt.Errorf("%w: logical root cannot be copied", storagecore.ErrForbidden)
	}
	data, revision, err := d.moveFileRevision(ctx, d.client, key)
	if err != nil {
		return "", err
	}
	if err := d.PutContext(ctx, dst, []byte(data)); err != nil {
		return "", err
	}
	return revision, nil
}

// DeleteMoveFileContext uses the existing WATCH transaction to preserve a concurrently rewritten source.
func (d *driver) DeleteMoveFileContext(ctx context.Context, src, revision string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(src)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" || revision == "" {
		return fmt.Errorf("%w: conditional deletion requires an object path and revision", storagecore.ErrForbidden)
	}
	return d.deleteObjectRevision(ctx, key, revision)
}

// moveFileRevision uses the existing hash layout, including legacy objects that lack a modification timestamp.
func (d *driver) moveFileRevision(ctx context.Context, client redis.Cmdable, key string) (string, string, error) {
	fields, err := client.HMGet(ctx, d.objectKey(key), "data", "modtime").Result()
	if err != nil {
		return "", "", err
	}
	data, ok := fields[0].(string)
	if !ok {
		return "", "", storagecore.ErrNotFound
	}
	modified, _ := fields[1].(string)
	return data, fmt.Sprintf("%x:%s", sha256.Sum256([]byte(data)), modified), nil
}
