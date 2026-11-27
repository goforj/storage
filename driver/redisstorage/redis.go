package redisstorage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
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
	return newFromDiskConfig(context.Background(), cfg.ResolvedConfig())
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

	client := redis.NewClient(redisClientOptions(cfg))
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, joinCleanup(fmt.Errorf("storage: redis ping: %w", err), client.Close())
	}

	return &driver{
		client:    client,
		namespace: redisNamespace(cfg),
		prefix:    prefix,
	}, nil
}

// redisClientOptions preserves the connection and retry behavior used before go-redis changed its defaults.
func redisClientOptions(cfg storagecore.ResolvedConfig) *redis.Options {
	options := &redis.Options{
		Addr:            cfg.RedisAddr,
		Username:        cfg.RedisUsername,
		Password:        cfg.RedisPassword,
		DB:              cfg.RedisDB,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		PoolTimeout:     4 * time.Second,
		MinRetryBackoff: 8 * time.Millisecond,
		MaxRetryBackoff: 512 * time.Millisecond,
	}
	options.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 5 * time.Minute,
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return options
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

// redisNamespace isolates metadata by configured database while preserving the original DB-zero prefix.
func redisNamespace(cfg storagecore.ResolvedConfig) string {
	base := "goforj:storage:redis"
	if cfg.RedisDB != 0 {
		base += ":db:" + strconv.Itoa(cfg.RedisDB)
	}
	return base
}
