package redisstorage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/storage/storagecore"
	"github.com/redis/go-redis/v9"
)

// init registers package integration before callers construct storage.
func init() {
	storagecore.RegisterDriver("redis", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client    redis.UniversalClient
	namespace string
	prefix    string
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

type indexSchema uint8

const (
	legacyIndex indexSchema = iota
	versionedIndex
)

type indexedMemberRecord struct {
	member string
	schema indexSchema
}

type indexedCandidate struct {
	key   string
	isDir bool
}

// normalizeContext keeps context methods nil-safe while remaining compatible with released storagecore versions.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var errDeleteRedispatch = errors.New("storage: redis delete state changed")

const objectSizeScript = `
if redis.call("HEXISTS", KEYS[1], ARGV[1]) == 0 then
  return -1
end
return redis.call("HSTRLEN", KEYS[1], ARGV[1])
`

// Config defines a Redis-backed storage disk for distributed temporary blobs.
// @group Driver Config
//
// Example: define redis storage config
//
//	cfg := redisstorage.Config{
//		Addr: "127.0.0.1:6379",
//	}
//	_ = cfg
//
// Example: define redis storage config with all fields
//
//	cfg := redisstorage.Config{
//		Addr:     "127.0.0.1:6379",
//		Username: "",
//		Password: "",
//		DB:       0,
//		Prefix:   "scratch", // default: ""
//	}
//	_ = cfg
type Config struct {
	Addr     string
	Username string
	Password string
	DB       int
	Prefix   string
}

// DriverName returns the registry key used for Redis configurations.
func (Config) DriverName() string { return "redis" }

// ResolvedConfig translates Redis-specific fields into the shared build payload.
func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
		Driver:        "redis",
		RedisAddr:     c.Addr,
		RedisUsername: c.Username,
		RedisPassword: c.Password,
		RedisDB:       c.DB,
		Prefix:        c.Prefix,
	}
}

// New constructs Redis-backed storage using go-redis.
// @group Driver Constructors
//
// Example: redis storage
//
//	fs, _ := redisstorage.New(redisstorage.Config{
//		Addr:   "127.0.0.1:6379",
//		Prefix: "scratch",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

// NewContext constructs Redis-backed storage while honoring startup cancellation.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig validates shared configuration and verifies the Redis connection before returning it.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.RedisAddr == "" {
		return nil, fmt.Errorf("storage: redis storage requires RedisAddr")
	}
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Username: cfg.RedisUsername,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, joinCleanup(fmt.Errorf("storage: redis ping: %w", err), client.Close())
	}

	return &driver{
		client:    client,
		namespace: redisNamespace(cfg),
		prefix:    prefix,
	}, nil
}

// Get reads an object using a background context.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext reads the object payload stored in its Redis hash.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	if d.stripPrefix(key) == "" {
		return nil, fmt.Errorf("%w: logical root cannot be read as an object", storagecore.ErrForbidden)
	}
	fields, err := d.client.HMGet(ctx, d.objectKey(key), "data", "modtime").Result()
	if err != nil {
		return nil, fmt.Errorf("storage: redis get: %w", err)
	}
	if len(fields) == 0 || fields[0] == nil {
		return nil, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	data, ok := fields[0].(string)
	if !ok {
		return nil, fmt.Errorf("storage: redis get: unexpected payload type %T", fields[0])
	}
	return []byte(data), nil
}

// Put stores an object using a background context.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir records an explicit directory using a background context.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext commits an object hash and its hierarchy indexes in one optimistic transaction.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	modTime := time.Now().UTC().UnixNano()
	err = d.watchTransaction(ctx, d.objectCollisionKeys(key), func(tx *redis.Tx) error {
		if err := d.ensureObjectPath(ctx, tx, key); err != nil {
			return err
		}
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, d.objectKey(key), map[string]any{
				"data":    string(contents),
				"modtime": modTime,
			})
			d.indexPut(ctx, pipe, key)
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("storage: redis put: %w", err)
	}
	return nil
}

// MakeDirContext records an explicit directory after atomically rejecting path collisions.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" {
		return nil
	}
	err = d.watchTransaction(ctx, d.directoryCollisionKeys(key), func(tx *redis.Tx) error {
		if err := d.ensureDirectoryPath(ctx, tx, key); err != nil {
			return err
		}
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			d.indexDir(ctx, pipe, key)
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("storage: redis mkdir: %w", err)
	}
	return nil
}

// Delete removes an object or empty directory using a background context.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext redispatches between object and directory deletion if concurrent state changes.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" {
		return fmt.Errorf("%w: logical root cannot be deleted", storagecore.ErrForbidden)
	}
	for {
		isObject, err := d.client.HExists(ctx, d.objectKey(key), "data").Result()
		if err != nil {
			return fmt.Errorf("storage: redis delete: %w", err)
		}
		if isObject {
			err = d.deleteObjectContext(ctx, key)
		} else {
			err = d.deleteDirectoryContext(ctx, key)
		}
		if errors.Is(err, errDeleteRedispatch) {
			continue
		}
		if err != nil {
			return fmt.Errorf("storage: redis delete: %w", err)
		}
		return nil
	}
}

// Stat returns object or directory metadata using a background context.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext probes object data before consulting verified directory indexes.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return storagecore.Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if d.stripPrefix(key) == "" {
		return storagecore.Entry{Path: "", IsDir: true}, nil
	}
	size, err := d.objectSize(ctx, key)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if size >= 0 {
		return storagecore.Entry{Path: d.stripPrefix(key), Size: size, IsDir: false}, nil
	}

	ok, err := d.dirExists(ctx, key)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if ok {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	}
	return storagecore.Entry{}, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
}

// Exists reports object existence using a background context.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext reports whether an object hash has a data field.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := d.key(p)
	if err != nil {
		return false, err
	}
	if d.stripPrefix(key) == "" {
		return false, nil
	}
	size, err := d.objectSize(ctx, key)
	if err != nil {
		return false, err
	}
	return size >= 0, nil
}

// List returns immediate children using a background context.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage returns a deterministic window of immediate children.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext merges both index generations and omits entries without valid backing state.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	children, err := d.children(ctx, key)
	if err != nil {
		return nil, err
	}
	entries, err := d.listEntries(ctx, children)
	if err != nil {
		return nil, err
	}
	if key != "" && key != d.prefix && len(entries) == 0 {
		size, err := d.objectSize(ctx, key)
		if err != nil {
			return nil, err
		}
		if size >= 0 {
			return nil, fmt.Errorf("%w: path is not a directory", storagecore.ErrNotFound)
		}
		ok, err := d.dirExists(ctx, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
		}
	}
	return entries, nil
}

// ListPageContext paginates the sorted result returned by ListContext.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

// Walk visits verified descendants using a background context.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext visits verified descendants in path order and stops on cancellation or callback error.
func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("%w: walk callback is required", storagecore.ErrForbidden)
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	keys, err := d.descendants(ctx, key)
	if err != nil {
		return err
	}
	entries, ok, err := d.walkEntries(ctx, keys, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return nil
}

// Copy duplicates an object using a background context.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext atomically validates the source and destination before indexing a new object hash.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	srcKey, err := d.key(src)
	if err != nil {
		return err
	}
	dstKey, err := d.key(dst)
	if err != nil {
		return err
	}
	if d.stripPrefix(srcKey) == "" || d.stripPrefix(dstKey) == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	watchKeys := append(d.objectCollisionKeys(dstKey), d.objectKey(srcKey))
	err = d.watchTransaction(ctx, watchKeys, func(tx *redis.Tx) error {
		fields, err := tx.HGetAll(ctx, d.objectKey(srcKey)).Result()
		if err != nil {
			return err
		}
		data, ok := fields["data"]
		if !ok {
			return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
		}
		if srcKey == dstKey {
			return nil
		}
		if err := d.ensureObjectPath(ctx, tx, dstKey); err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, d.objectKey(dstKey), map[string]any{
				"data":    data,
				"modtime": time.Now().UTC().UnixNano(),
			})
			d.indexPut(ctx, pipe, dstKey)
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("storage: redis copy: %w", err)
	}
	return nil
}

// Move relocates an object or directory tree using a background context.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext moves objects atomically and delegates directory rollback semantics to storagecore.
func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	srcPath, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	dstPath, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("%w: logical root cannot be moved", storagecore.ErrForbidden)
	}
	srcEntry, err := d.StatContext(ctx, src)
	if err != nil {
		return err
	}
	if srcPath == dstPath {
		return nil
	}
	if srcEntry.IsDir {
		return storagecore.MoveDirContext(ctx, d, src, dst)
	}
	srcKey, err := d.key(src)
	if err != nil {
		return err
	}
	dstKey, err := d.key(dst)
	if err != nil {
		return err
	}
	if d.stripPrefix(dstKey) == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	watchKeys := append(d.objectCollisionKeys(dstKey), d.objectKey(srcKey))
	err = d.watchTransaction(ctx, watchKeys, func(tx *redis.Tx) error {
		fields, err := tx.HGetAll(ctx, d.objectKey(srcKey)).Result()
		if err != nil {
			return err
		}
		data, ok := fields["data"]
		if !ok {
			return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
		}
		if err := d.ensureObjectPath(ctx, tx, dstKey); err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, d.objectKey(dstKey), map[string]any{
				"data":    data,
				"modtime": time.Now().UTC().UnixNano(),
			})
			d.indexPut(ctx, pipe, dstKey)
			d.queueUnindexDelete(ctx, pipe, srcKey)
			d.queuePruneEmptyDirs(ctx, pipe, objectDirs(srcKey))
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("storage: redis move: %w", err)
	}
	return nil
}

// URL validates a path before reporting that Redis cannot expose public object URLs.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext honors cancellation and distinguishes missing objects from unsupported URL generation.
func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := d.StatContext(ctx, p); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%w: public URL not supported for redis", storagecore.ErrUnsupported)
}

// ModTime returns the object's mod time. Intended for testing only.
func (d *driver) ModTime(ctx context.Context, p string) (time.Time, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return time.Time{}, err
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return time.Time{}, err
	}
	if d.stripPrefix(key) == "" {
		return time.Time{}, fmt.Errorf("%w: logical root has no object modification time", storagecore.ErrForbidden)
	}
	raw, err := d.client.HGet(ctx, d.objectKey(key), "modtime").Result()
	if err == redis.Nil {
		return time.Time{}, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("storage: redis modtime: %w", err)
	}
	nanos, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("storage: redis modtime parse: %w", err)
	}
	return time.Unix(0, nanos).UTC(), nil
}

// Close releases the Redis client once and makes every later operation return fs.ErrClosed.
func (d *driver) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		if d.client != nil {
			d.closeErr = d.client.Close()
		}
	})
	return d.closeErr
}

// closedError enforces the driver's terminal lifecycle state before client access.
func (d *driver) closedError() error {
	if d.closed.Load() {
		return fmt.Errorf("storage: redis: %w", fs.ErrClosed)
	}
	return nil
}

// joinCleanup preserves both a startup failure and any client-close failure.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// key normalizes a caller path and places it beneath the configured logical prefix.
func (d *driver) key(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// objectKey maps one logical object to its namespaced Redis hash.
func (d *driver) objectKey(key string) string {
	return d.namespace + ":obj:" + key
}

// dirChildrenKey maps a directory to the released mixed-schema child set.
func (d *driver) dirChildrenKey(key string) string {
	return d.namespace + ":dir:" + key + ":children"
}

// dirObjectsKey maps a directory to the released mixed-schema descendant set.
func (d *driver) dirObjectsKey(key string) string {
	return d.namespace + ":dir:" + key + ":objects"
}

// versionedDirChildrenKey isolates typed child metadata from ambiguous released legacy sets.
func (d *driver) versionedDirChildrenKey(key string) string {
	return d.namespace + ":index:v2:dir:" + base64.RawURLEncoding.EncodeToString([]byte(key)) + ":children"
}

// versionedDirObjectsKey isolates typed descendant metadata from raw legacy object names.
func (d *driver) versionedDirObjectsKey(key string) string {
	return d.namespace + ":index:v2:dir:" + base64.RawURLEncoding.EncodeToString([]byte(key)) + ":objects"
}

// stripPrefix converts an internal key back to its caller-visible path.
func (d *driver) stripPrefix(key string) string {
	if d.prefix == "" {
		return key
	}
	if key == d.prefix {
		return ""
	}
	return strings.TrimPrefix(key, d.prefix+"/")
}

// objectSize atomically distinguishes a missing payload from a valid empty object.
func (d *driver) objectSize(ctx context.Context, key string) (int64, error) {
	return d.objectSizeWithClient(ctx, d.client, key)
}

// objectSizeWithClient keeps transactional validation on the connection that owns the WATCH state.
func (d *driver) objectSizeWithClient(ctx context.Context, client redis.Cmdable, key string) (int64, error) {
	size, err := client.Eval(ctx, objectSizeScript, []string{d.objectKey(key)}, "data").Int64()
	if err != nil {
		return -1, fmt.Errorf("storage: redis stat: %w", err)
	}
	return size, nil
}

// deleteObjectContext makes source validation and index removal one optimistic transaction.
func (d *driver) deleteObjectContext(ctx context.Context, key string) error {
	return d.watchTransaction(ctx, []string{d.objectKey(key)}, func(tx *redis.Tx) error {
		exists, err := tx.HExists(ctx, d.objectKey(key), "data").Result()
		if err != nil {
			return err
		}
		if !exists {
			return errDeleteRedispatch
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

// children merges both index generations and drops links whose backing state no longer exists.
func (d *driver) children(ctx context.Context, key string) ([]string, error) {
	return d.childrenSeen(ctx, key, make(map[string]struct{}))
}

// childrenSeen shares recursion state while validating directory child links.
func (d *driver) childrenSeen(ctx context.Context, key string, seen map[string]struct{}) ([]string, error) {
	sources, err := d.childMembersWithClient(ctx, d.client, key)
	if err != nil {
		return nil, err
	}
	children := make([]string, 0, len(sources))
	for _, child := range sources {
		isDir, childKey, err := parseChildEntry(child)
		if err != nil || childKey == key || parentDir(childKey) != key {
			continue
		}
		valid := false
		if isDir {
			valid, err = d.dirExistsSeen(ctx, childKey, seen)
		} else {
			var size int64
			size, err = d.objectSize(ctx, childKey)
			valid = size >= 0
		}
		if err != nil {
			return nil, err
		}
		if valid {
			children = append(children, child)
		}
	}
	return children, nil
}

// childMembersWithClient returns the sorted union of released and v2 child links without trusting either.
func (d *driver) childMembersWithClient(ctx context.Context, client redis.Cmdable, key string) ([]string, error) {
	pipe := client.Pipeline()
	legacy := pipe.SMembers(ctx, d.dirChildrenKey(key))
	versioned := pipe.SMembers(ctx, d.versionedDirChildrenKey(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("storage: redis list: %w", err)
	}
	sources := map[string]struct{}{}
	for _, child := range legacy.Val() {
		sources[child] = struct{}{}
	}
	for _, child := range versioned.Val() {
		sources[child] = struct{}{}
	}
	children := make([]string, 0, len(sources))
	for child := range sources {
		children = append(children, child)
	}
	slices.Sort(children)
	return children, nil
}

// listEntries converts verified child members into sorted public entries.
func (d *driver) listEntries(ctx context.Context, children []string) ([]storagecore.Entry, error) {
	entries := make([]storagecore.Entry, 0, len(children))
	for _, child := range children {
		isDir, key, err := parseChildEntry(child)
		if err != nil {
			return nil, err
		}
		entry := storagecore.Entry{Path: d.stripPrefix(key), IsDir: isDir}
		if isDir {
			exists, err := d.dirExists(ctx, key)
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
		} else {
			size, err := d.objectSize(ctx, key)
			if err != nil {
				return nil, err
			}
			if size < 0 {
				continue
			}
			entry.Size = size
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

// walkEntries emits only verified descendants, so stale indexes cannot synthesize ghost parents.
func (d *driver) walkEntries(ctx context.Context, records []indexedMemberRecord, key string) ([]storagecore.Entry, bool, error) {
	if d.stripPrefix(key) != "" {
		size, err := d.objectSize(ctx, key)
		if err != nil {
			return nil, false, err
		}
		if size >= 0 {
			return []storagecore.Entry{{Path: d.stripPrefix(key), Size: size, IsDir: false}}, true, nil
		}
	}
	found := d.stripPrefix(key) == ""
	if !found {
		var err error
		found, err = d.dirExists(ctx, key)
		if err != nil {
			return nil, false, err
		}
	}

	seenDirs := map[string]struct{}{}
	seenObjects := map[string]struct{}{}
	entries := make([]storagecore.Entry, 0, len(records))
	requested := d.stripPrefix(key)
	for _, record := range records {
		candidates, err := d.resolveIndexedMember(ctx, record)
		if err != nil {
			return nil, false, err
		}
		for _, candidate := range candidates {
			existing := candidate.key
			if key != "" && existing != key && !strings.HasPrefix(existing, key+"/") {
				continue
			}
			if candidate.isDir {
				dir := d.stripPrefix(existing)
				if dir == "" || dir == requested {
					continue
				}
				for _, parent := range recursiveParentDirs(dir + "/marker") {
					if parent == requested || (requested != "" && !strings.HasPrefix(parent, requested+"/")) {
						continue
					}
					fullDir := storagecore.JoinPrefix(d.prefix, parent)
					if _, ok := seenDirs[fullDir]; ok {
						continue
					}
					seenDirs[fullDir] = struct{}{}
					entries = append(entries, storagecore.Entry{Path: parent, IsDir: true})
				}
				continue
			}
			if _, ok := seenObjects[existing]; ok {
				continue
			}
			seenObjects[existing] = struct{}{}
			for _, dir := range recursiveParentDirs(d.stripPrefix(existing)) {
				if dir == requested || (requested != "" && !strings.HasPrefix(dir, requested+"/")) {
					continue
				}
				fullDir := storagecore.JoinPrefix(d.prefix, dir)
				if _, ok := seenDirs[fullDir]; ok {
					continue
				}
				seenDirs[fullDir] = struct{}{}
				entries = append(entries, storagecore.Entry{Path: dir, IsDir: true})
			}
			size, err := d.objectSize(ctx, existing)
			if err != nil {
				return nil, false, err
			}
			if size < 0 {
				continue
			}
			entries = append(entries, storagecore.Entry{Path: d.stripPrefix(existing), Size: size})
		}
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, found, nil
}

// descendants returns records with schema provenance so ambiguous legacy values can be resolved safely.
func (d *driver) descendants(ctx context.Context, key string) ([]indexedMemberRecord, error) {
	return d.descendantsWithClient(ctx, d.client, key)
}

// descendantsWithClient reads both schema generations through one client or watched transaction.
func (d *driver) descendantsWithClient(ctx context.Context, client redis.Cmdable, key string) ([]indexedMemberRecord, error) {
	pipe := client.Pipeline()
	legacy := pipe.SMembers(ctx, d.dirObjectsKey(key))
	versioned := pipe.SMembers(ctx, d.versionedDirObjectsKey(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("storage: redis walk: %w", err)
	}
	records := make([]indexedMemberRecord, 0, len(legacy.Val())+len(versioned.Val()))
	for _, member := range legacy.Val() {
		records = append(records, indexedMemberRecord{member: member, schema: legacyIndex})
	}
	for _, member := range versioned.Val() {
		records = append(records, indexedMemberRecord{member: member, schema: versionedIndex})
	}
	slices.SortFunc(records, func(a, b indexedMemberRecord) int {
		if order := strings.Compare(a.member, b.member); order != 0 {
			return order
		}
		return int(a.schema) - int(b.schema)
	})
	return records, nil
}

// dirExists accepts only markers or descendants whose backing state validates.
func (d *driver) dirExists(ctx context.Context, key string) (bool, error) {
	return d.dirExistsSeen(ctx, key, make(map[string]struct{}))
}

// dirExistsSeen bounds recursive child-only visibility when Redis metadata is corrupt or adversarial.
func (d *driver) dirExistsSeen(ctx context.Context, key string, seen map[string]struct{}) (bool, error) {
	if _, ok := seen[key]; ok {
		return false, nil
	}
	if len(seen) >= 1024 {
		return false, nil
	}
	seen[key] = struct{}{}
	defer delete(seen, key)

	records, err := d.descendants(ctx, key)
	if err != nil {
		return false, err
	}
	exists, _, err := d.directoryStateFromRecords(ctx, d.client, key, records)
	if err != nil || exists {
		return exists, err
	}
	children, err := d.childrenSeen(ctx, key, seen)
	if err != nil {
		return false, err
	}
	return len(children) != 0, nil
}

// directoryStateInTransaction watches every backing key implied by a stable index snapshot.
func (d *driver) directoryStateInTransaction(ctx context.Context, tx *redis.Tx, key string) (bool, bool, error) {
	return d.directoryStateInTransactionSeen(ctx, tx, key, make(map[string]struct{}))
}

// directoryStateInTransactionSeen mirrors recursive public visibility while rejecting malicious index cycles.
func (d *driver) directoryStateInTransactionSeen(ctx context.Context, tx *redis.Tx, key string, seen map[string]struct{}) (bool, bool, error) {
	if _, ok := seen[key]; ok {
		return false, false, fmt.Errorf("%w: cyclic directory child index at %q", storagecore.ErrForbidden, d.stripPrefix(key))
	}
	if len(seen) >= 1024 {
		return false, false, fmt.Errorf("%w: directory child index exceeds maximum validation depth", storagecore.ErrForbidden)
	}
	seen[key] = struct{}{}
	defer delete(seen, key)

	exists, hasContents, err := d.directoryRecordsStateInTransaction(ctx, tx, key)
	if err != nil {
		return false, false, err
	}
	children, err := d.watchedChildMembers(ctx, tx, key)
	if err != nil {
		return false, false, err
	}
	for _, child := range children {
		isDir, childKey, err := parseChildEntry(child)
		if err != nil || childKey == key || parentDir(childKey) != key {
			continue
		}
		valid := false
		if isDir {
			valid, _, err = d.directoryStateInTransactionSeen(ctx, tx, childKey, seen)
		} else {
			var size int64
			size, err = d.objectSizeWithClient(ctx, tx, childKey)
			valid = size >= 0
		}
		if err != nil {
			return false, false, err
		}
		if valid {
			exists = true
			hasContents = true
		}
	}
	return exists, hasContents, nil
}

// directoryRecordsStateInTransaction validates descendant records without recursing through child links.
func (d *driver) directoryRecordsStateInTransaction(ctx context.Context, tx *redis.Tx, key string) (bool, bool, error) {
	records, err := d.watchedDescendants(ctx, tx, key)
	if err != nil {
		return false, false, err
	}
	return d.directoryStateFromRecords(ctx, tx, key, records)
}

// watchedChildMembers watches child sets and object hashes before returning a stable link snapshot.
func (d *driver) watchedChildMembers(ctx context.Context, tx *redis.Tx, key string) ([]string, error) {
	indexKeys := []string{d.dirChildrenKey(key), d.versionedDirChildrenKey(key)}
	if err := tx.Watch(ctx, indexKeys...).Err(); err != nil {
		return nil, fmt.Errorf("storage: redis watch directory children: %w", err)
	}
	children, err := d.childMembersWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	watchKeys := make([]string, 0, len(children))
	for _, child := range children {
		isDir, childKey, err := parseChildEntry(child)
		if err == nil && !isDir && parentDir(childKey) == key {
			watchKeys = append(watchKeys, d.objectKey(childKey))
		}
	}
	if len(watchKeys) != 0 {
		slices.Sort(watchKeys)
		watchKeys = slices.Compact(watchKeys)
		if err := tx.Watch(ctx, watchKeys...).Err(); err != nil {
			return nil, fmt.Errorf("storage: redis watch child objects: %w", err)
		}
	}
	stable, err := d.childMembersWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(children, stable) {
		return nil, redis.TxFailedErr
	}
	return stable, nil
}

// watchedDescendants closes stale-index races by watching candidate state before validation.
func (d *driver) watchedDescendants(ctx context.Context, tx *redis.Tx, key string) ([]indexedMemberRecord, error) {
	indexKeys := []string{d.dirObjectsKey(key), d.versionedDirObjectsKey(key)}
	if err := tx.Watch(ctx, indexKeys...).Err(); err != nil {
		return nil, fmt.Errorf("storage: redis watch directory indexes: %w", err)
	}
	records, err := d.descendantsWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	watchKeys := d.indexedMemberWatchKeys(records)
	if len(watchKeys) != 0 {
		if err := tx.Watch(ctx, watchKeys...).Err(); err != nil {
			return nil, fmt.Errorf("storage: redis watch indexed state: %w", err)
		}
	}
	stable, err := d.descendantsWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(records, stable) {
		return nil, redis.TxFailedErr
	}
	return stable, nil
}

// indexedMemberWatchKeys derives every hash or marker link consulted while resolving records.
func (d *driver) indexedMemberWatchKeys(records []indexedMemberRecord) []string {
	keys := map[string]struct{}{}
	addObject := func(key string) {
		keys[d.objectKey(key)] = struct{}{}
	}
	addMarker := func(schema indexSchema, key string) {
		if schema == versionedIndex {
			keys[d.versionedDirObjectsKey(key)] = struct{}{}
			keys[d.versionedDirChildrenKey(parentDir(key))] = struct{}{}
			return
		}
		keys[d.dirObjectsKey(key)] = struct{}{}
		keys[d.dirChildrenKey(parentDir(key))] = struct{}{}
	}
	for _, record := range records {
		if record.schema == legacyIndex {
			addObject(record.member)
		}
		key, marker, err := parseIndexedMember(record.member)
		if err != nil {
			continue
		}
		if marker {
			addMarker(record.schema, key)
			continue
		}
		addObject(key)
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// directoryStateFromRecords reports directory existence and whether a verified descendant exists.
func (d *driver) directoryStateFromRecords(ctx context.Context, client redis.Cmdable, key string, records []indexedMemberRecord) (bool, bool, error) {
	exists := key == ""
	hasContents := false
	for _, record := range records {
		candidates, err := d.resolveIndexedMemberWithClient(ctx, client, record)
		if err != nil {
			return false, false, err
		}
		for _, candidate := range candidates {
			descendant := key == "" || strings.HasPrefix(candidate.key, key+"/")
			if descendant {
				exists = true
				hasContents = true
			}
			if candidate.isDir {
				if candidate.key == key {
					exists = true
				}
			}
		}
	}
	return exists, hasContents, nil
}

// resolveIndexedMember disambiguates legacy raw names by checking every possible backing object.
func (d *driver) resolveIndexedMember(ctx context.Context, record indexedMemberRecord) ([]indexedCandidate, error) {
	return d.resolveIndexedMemberWithClient(ctx, d.client, record)
}

// resolveIndexedMemberWithClient validates an index candidate on the transaction connection when required.
func (d *driver) resolveIndexedMemberWithClient(ctx context.Context, client redis.Cmdable, record indexedMemberRecord) ([]indexedCandidate, error) {
	if record.schema == versionedIndex {
		key, marker, err := parseIndexedMember(record.member)
		if err != nil {
			return nil, err
		}
		if marker {
			valid, err := d.versionedDirectoryMarkerValidWithClient(ctx, client, key)
			if err != nil || !valid {
				return nil, err
			}
			return []indexedCandidate{{key: key, isDir: true}}, nil
		}
		size, err := d.objectSizeWithClient(ctx, client, key)
		if err != nil || size < 0 {
			return nil, err
		}
		return []indexedCandidate{{key: key}}, nil
	}

	candidates := make([]indexedCandidate, 0, 2)
	seen := map[indexedCandidate]struct{}{}
	addObject := func(key string) error {
		size, err := d.objectSizeWithClient(ctx, client, key)
		if err != nil {
			return err
		}
		if size >= 0 {
			candidate := indexedCandidate{key: key}
			if _, ok := seen[candidate]; !ok {
				seen[candidate] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}
		return nil
	}
	if err := addObject(record.member); err != nil {
		return nil, err
	}
	key, marker, err := parseIndexedMember(record.member)
	if err != nil {
		if len(candidates) != 0 {
			return candidates, nil
		}
		return nil, nil
	}
	if !marker {
		if err := addObject(key); err != nil {
			return nil, err
		}
		return candidates, nil
	}
	valid, err := d.legacyDirectoryMarkerValidWithClient(ctx, client, key)
	if err != nil {
		return nil, err
	}
	if valid {
		candidate := indexedCandidate{key: key, isDir: true}
		if _, ok := seen[candidate]; !ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

// legacyDirectoryMarkerValid requires the released schema's self marker and parent link to agree.
func (d *driver) legacyDirectoryMarkerValid(ctx context.Context, key string) (bool, error) {
	return d.legacyDirectoryMarkerValidWithClient(ctx, d.client, key)
}

// legacyDirectoryMarkerValidWithClient treats the exact parent link as the deletion and recreation authority.
func (d *driver) legacyDirectoryMarkerValidWithClient(ctx context.Context, client redis.Cmdable, key string) (bool, error) {
	pipe := client.Pipeline()
	self := pipe.SMembers(ctx, d.dirObjectsKey(key))
	parent := pipe.SIsMember(ctx, d.dirChildrenKey(parentDir(key)), encodeDirChild(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("storage: redis stat: %w", err)
	}
	hasSelfMarker := slices.Contains(self.Val(), dirMarkerMember(key)) || slices.Contains(self.Val(), legacyDirMarkerMember(key))
	return hasSelfMarker && parent.Val(), nil
}

// versionedDirectoryMarkerValid requires both links written by the v2 directory transaction.
func (d *driver) versionedDirectoryMarkerValid(ctx context.Context, key string) (bool, error) {
	return d.versionedDirectoryMarkerValidWithClient(ctx, d.client, key)
}

// versionedDirectoryMarkerValidWithClient validates both v2 links on one transaction connection.
func (d *driver) versionedDirectoryMarkerValidWithClient(ctx context.Context, client redis.Cmdable, key string) (bool, error) {
	marker := dirMarkerMember(key)
	pipe := client.Pipeline()
	self := pipe.SIsMember(ctx, d.versionedDirObjectsKey(key), marker)
	parent := pipe.SIsMember(ctx, d.versionedDirChildrenKey(parentDir(key)), encodeDirChild(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("storage: redis stat: %w", err)
	}
	return self.Val() && parent.Val(), nil
}

// dirMarkerMember encodes a v1 typed directory member without colliding with ordinary names.
func dirMarkerMember(key string) string {
	return "storage:v1:directory:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// legacyDirMarkerMember reproduces the unescaped marker written by early releases.
func legacyDirMarkerMember(key string) string {
	return "dirmarker:" + key
}

// objectMember encodes a typed object member for the v2-only index sets.
func objectMember(key string) string {
	return "storage:v1:object:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// parseIndexedMember decodes typed members while treating untagged values as legacy object names.
func parseIndexedMember(member string) (string, bool, error) {
	const (
		objectPrefix    = "storage:v1:object:"
		directoryPrefix = "storage:v1:directory:"
	)
	switch {
	case strings.HasPrefix(member, objectPrefix):
		key, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(member, objectPrefix))
		if err != nil {
			return "", false, fmt.Errorf("storage: redis invalid object index member: %w", err)
		}
		return string(key), false, nil
	case strings.HasPrefix(member, directoryPrefix):
		key, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(member, directoryPrefix))
		if err != nil {
			return "", false, fmt.Errorf("storage: redis invalid directory index member: %w", err)
		}
		return string(key), true, nil
	case strings.HasPrefix(member, "dirmarker:"):
		return strings.TrimPrefix(member, "dirmarker:"), true, nil
	default:
		return member, false, nil
	}
}

// indexPut writes an object's hierarchy only to collision-free v2 index sets.
func (d *driver) indexPut(ctx context.Context, pipe redis.Pipeliner, key string) {
	dirs := objectDirs(key)
	member := objectMember(key)
	pipe.SAdd(ctx, d.versionedDirObjectsKey(""), member)
	if len(dirs) == 0 {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeFileChild(key))
		return
	}
	for _, dir := range dirs {
		pipe.SAdd(ctx, d.versionedDirObjectsKey(dir), member)
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeDirChild(dirs[0]))
	for i := 0; i < len(dirs)-1; i++ {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1]))
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[len(dirs)-1]), encodeFileChild(key))
}

// indexDir records explicit directories only in v2 so reserved-looking paths remain ordinary names.
func (d *driver) indexDir(ctx context.Context, pipe redis.Pipeliner, key string) {
	marker := dirMarkerMember(key)
	pipe.SAdd(ctx, d.versionedDirObjectsKey(""), marker)
	dirs := objectDirs(key)
	for _, dir := range dirs {
		pipe.SAdd(ctx, d.versionedDirObjectsKey(dir), marker)
	}
	pipe.SAdd(ctx, d.versionedDirObjectsKey(key), marker)
	if len(dirs) == 0 {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeDirChild(key))
		return
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeDirChild(dirs[0]))
	for i := 0; i < len(dirs)-1; i++ {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1]))
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[len(dirs)-1]), encodeDirChild(key))
}

// queueUnindexDelete removes v2 metadata and only legacy members proven unable to alias typed entries.
func (d *driver) queueUnindexDelete(ctx context.Context, pipe redis.Pipeliner, key string) {
	pipe.Del(ctx, d.objectKey(key))
	pipe.SRem(ctx, d.versionedDirObjectsKey(""), objectMember(key))
	pipe.SRem(ctx, d.versionedDirChildrenKey(parentDir(key)), encodeFileChild(key))
	for _, dir := range objectDirs(key) {
		pipe.SRem(ctx, d.versionedDirObjectsKey(dir), objectMember(key))
	}

	if legacy, ok := unambiguousLegacyObjectMember(key); ok {
		pipe.SRem(ctx, d.dirObjectsKey(""), legacy)
		pipe.SRem(ctx, d.dirChildrenKey(parentDir(key)), encodeFileChild(key))
		for _, dir := range objectDirs(key) {
			pipe.SRem(ctx, d.dirObjectsKey(dir), legacy)
		}
	}
}

// unambiguousLegacyObjectMember excludes names that can also be another entry's typed index member.
func unambiguousLegacyObjectMember(key string) (string, bool) {
	if strings.HasPrefix(key, "storage:v1:object:") ||
		strings.HasPrefix(key, "storage:v1:directory:") ||
		strings.HasPrefix(key, "dirmarker:") {
		return "", false
	}
	return key, true
}

const pruneEmptyDirsScript = `
local arg = 1
for key = 1, #KEYS, 3 do
  if redis.call("SCARD", KEYS[key]) ~= 0 or redis.call("SCARD", KEYS[key + 1]) ~= 0 then
    break
  end
  redis.call("DEL", KEYS[key], KEYS[key + 1])
  redis.call("SREM", KEYS[key + 2], ARGV[arg])
  arg = arg + 1
end
return 1
`

// queuePruneEmptyDirs prunes both schemas without mixing their cardinality decisions.
func (d *driver) queuePruneEmptyDirs(ctx context.Context, pipe redis.Pipeliner, dirs []string) {
	d.queuePruneEmptyDirsForSchema(ctx, pipe, dirs, versionedIndex)
	d.queuePruneEmptyDirsForSchema(ctx, pipe, dirs, legacyIndex)
}

// queuePruneEmptyDirsForSchema keeps parent cleanup within one index generation.
func (d *driver) queuePruneEmptyDirsForSchema(ctx context.Context, pipe redis.Pipeliner, dirs []string, schema indexSchema) {
	if len(dirs) == 0 {
		return
	}
	keys := make([]string, 0, len(dirs)*3)
	args := make([]any, 0, len(dirs))
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		objectsKey := d.dirObjectsKey(dir)
		childrenKey := d.dirChildrenKey(dir)
		parentChildrenKey := d.dirChildrenKey(parentDir(dir))
		if schema == versionedIndex {
			objectsKey = d.versionedDirObjectsKey(dir)
			childrenKey = d.versionedDirChildrenKey(dir)
			parentChildrenKey = d.versionedDirChildrenKey(parentDir(dir))
		}
		keys = append(keys, objectsKey, childrenKey, parentChildrenKey)
		args = append(args, encodeDirChild(dir))
	}
	pipe.Eval(ctx, pruneEmptyDirsScript, keys, args...)
}

// recursiveParentDirs returns visible ancestors for a caller-facing object path.
func recursiveParentDirs(p string) []string {
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := range parts[:len(parts)-1] {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// objectDirs returns internal ancestor keys from nearest root through direct parent.
func objectDirs(key string) []string {
	parts := strings.Split(key, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := range parts[:len(parts)-1] {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// parentDir returns the direct parent key or the logical root for top-level entries.
func parentDir(key string) string {
	if key == "" {
		return ""
	}
	idx := strings.LastIndexByte(key, '/')
	if idx == -1 {
		return ""
	}
	return key[:idx]
}

// encodeFileChild tags an object link in a directory child set.
func encodeFileChild(key string) string {
	return "f:" + key
}

// encodeDirChild tags a directory link in a directory child set.
func encodeDirChild(key string) string {
	return "d:" + key
}

// parseChildEntry separates a child-set type tag from its internal key.
func parseChildEntry(child string) (bool, string, error) {
	switch {
	case strings.HasPrefix(child, "f:"):
		return false, strings.TrimPrefix(child, "f:"), nil
	case strings.HasPrefix(child, "d:"):
		return true, strings.TrimPrefix(child, "d:"), nil
	default:
		return false, "", fmt.Errorf("storage: redis invalid child entry %q", child)
	}
}

// redisNamespace isolates metadata by configured database while preserving the original DB-zero prefix.
func redisNamespace(cfg storagecore.ResolvedConfig) string {
	base := "goforj:storage:redis"
	if cfg.RedisDB != 0 {
		base += ":db:" + strconv.Itoa(cfg.RedisDB)
	}
	return base
}
