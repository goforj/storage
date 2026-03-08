package redisstorage

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/storage"
	"github.com/redis/go-redis/v9"
)

func init() {
	storage.RegisterDriver("redis", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client    redis.UniversalClient
	namespace string
	prefix    string
}

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

func (Config) DriverName() string { return "redis" }

func (c Config) ResolvedConfig() storage.ResolvedConfig {
	return storage.ResolvedConfig{
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
func New(cfg Config) (storage.Storage, error) {
	return NewContext(context.Background(), cfg)
}

func NewContext(ctx context.Context, cfg Config) (storage.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	if cfg.RedisAddr == "" {
		return nil, fmt.Errorf("storage: redis storage requires RedisAddr")
	}
	prefix, err := storage.NormalizePath(cfg.Prefix)
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
		_ = client.Close()
		return nil, fmt.Errorf("storage: redis ping: %w", err)
	}

	return &driver{
		client:    client,
		namespace: redisNamespace(cfg),
		prefix:    prefix,
	}, nil
}

func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	fields, err := d.client.HMGet(ctx, d.objectKey(key), "data", "modtime").Result()
	if err != nil {
		return nil, fmt.Errorf("storage: redis get: %w", err)
	}
	if len(fields) == 0 || fields[0] == nil {
		return nil, fmt.Errorf("%w: object not found", storage.ErrNotFound)
	}
	data, ok := fields[0].(string)
	if !ok {
		return nil, fmt.Errorf("storage: redis get: unexpected payload type %T", fields[0])
	}
	return []byte(data), nil
}

func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	modTime := time.Now().UTC().UnixNano()
	pipe := d.client.TxPipeline()
	pipe.HSet(ctx, d.objectKey(key), map[string]any{
		"data":    string(contents),
		"modtime": modTime,
	})
	pipe.SAdd(ctx, d.indexKey(), key)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("storage: redis put: %w", err)
	}
	return nil
}

func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

func (d *driver) DeleteContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	deleted, err := d.client.Del(ctx, d.objectKey(key)).Result()
	if err != nil {
		return fmt.Errorf("storage: redis delete: %w", err)
	}
	if deleted == 0 {
		return fmt.Errorf("%w: object not found", storage.ErrNotFound)
	}
	if err := d.client.SRem(ctx, d.indexKey(), key).Err(); err != nil {
		return fmt.Errorf("storage: redis delete: %w", err)
	}
	return nil
}

func (d *driver) Stat(p string) (storage.Entry, error) {
	return d.StatContext(context.Background(), p)
}

func (d *driver) StatContext(ctx context.Context, p string) (storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storage.Entry{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return storage.Entry{}, err
	}
	size, err := d.objectSize(ctx, key)
	if err != nil {
		return storage.Entry{}, err
	}
	if size >= 0 {
		return storage.Entry{Path: d.stripPrefix(key), Size: size, IsDir: false}, nil
	}

	keys, err := d.keys(ctx)
	if err != nil {
		return storage.Entry{}, err
	}
	if hasChildren(keys, key) {
		return storage.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	}
	return storage.Entry{}, fmt.Errorf("%w: object not found", storage.ErrNotFound)
}

func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := d.key(p)
	if err != nil {
		return false, err
	}
	size, err := d.objectSize(ctx, key)
	if err != nil {
		return false, err
	}
	return size >= 0, nil
}

func (d *driver) List(p string) ([]storage.Entry, error) {
	return d.ListContext(context.Background(), p)
}

func (d *driver) ListContext(ctx context.Context, p string) ([]storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	keys, err := d.keys(ctx)
	if err != nil {
		return nil, err
	}
	entries := d.listEntries(keys, key)
	if key != "" && len(entries) == 0 {
		size, err := d.objectSize(ctx, key)
		if err != nil {
			return nil, err
		}
		if size >= 0 {
			return nil, fmt.Errorf("%w: path is not a directory", storage.ErrNotFound)
		}
		if !hasChildren(keys, key) {
			return nil, fmt.Errorf("%w: object not found", storage.ErrNotFound)
		}
	}
	return entries, nil
}

func (d *driver) Walk(p string, fn func(storage.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

func (d *driver) WalkContext(ctx context.Context, p string, fn func(storage.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	keys, err := d.keys(ctx)
	if err != nil {
		return err
	}
	entries, ok, err := d.walkEntries(ctx, keys, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: object not found", storage.ErrNotFound)
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

func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
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
	fields, err := d.client.HGetAll(ctx, d.objectKey(srcKey)).Result()
	if err != nil {
		return fmt.Errorf("storage: redis copy: %w", err)
	}
	data, ok := fields["data"]
	if !ok {
		return fmt.Errorf("%w: object not found", storage.ErrNotFound)
	}
	pipe := d.client.TxPipeline()
	pipe.HSet(ctx, d.objectKey(dstKey), map[string]any{
		"data":    data,
		"modtime": time.Now().UTC().UnixNano(),
	})
	pipe.SAdd(ctx, d.indexKey(), dstKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("storage: redis copy: %w", err)
	}
	return nil
}

func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
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
	fields, err := d.client.HGetAll(ctx, d.objectKey(srcKey)).Result()
	if err != nil {
		return fmt.Errorf("storage: redis move: %w", err)
	}
	data, ok := fields["data"]
	if !ok {
		return fmt.Errorf("%w: object not found", storage.ErrNotFound)
	}
	pipe := d.client.TxPipeline()
	pipe.HSet(ctx, d.objectKey(dstKey), map[string]any{
		"data":    data,
		"modtime": time.Now().UTC().UnixNano(),
	})
	pipe.SAdd(ctx, d.indexKey(), dstKey)
	pipe.Del(ctx, d.objectKey(srcKey))
	pipe.SRem(ctx, d.indexKey(), srcKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("storage: redis move: %w", err)
	}
	return nil
}

func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := d.StatContext(ctx, p); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%w: public URL not supported for redis", storage.ErrUnsupported)
}

// ModTime returns the object's mod time. Intended for testing only.
func (d *driver) ModTime(ctx context.Context, p string) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return time.Time{}, err
	}
	raw, err := d.client.HGet(ctx, d.objectKey(key), "modtime").Result()
	if err == redis.Nil {
		return time.Time{}, fmt.Errorf("%w: object not found", storage.ErrNotFound)
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

func (d *driver) Close() error {
	return d.client.Close()
}

func (d *driver) key(p string) (string, error) {
	normalized, err := storage.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storage.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) objectKey(key string) string {
	return d.namespace + ":obj:" + key
}

func (d *driver) indexKey() string {
	return d.namespace + ":index"
}

func (d *driver) stripPrefix(key string) string {
	if d.prefix == "" {
		return key
	}
	trimmed := strings.TrimPrefix(key, d.prefix)
	return strings.TrimPrefix(trimmed, "/")
}

func (d *driver) objectSize(ctx context.Context, key string) (int64, error) {
	data, err := d.client.HGet(ctx, d.objectKey(key), "data").Bytes()
	if err == redis.Nil {
		return -1, nil
	}
	if err != nil {
		return -1, fmt.Errorf("storage: redis stat: %w", err)
	}
	return int64(len(data)), nil
}

func (d *driver) keys(ctx context.Context) ([]string, error) {
	keys, err := d.client.SMembers(ctx, d.indexKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("storage: redis list: %w", err)
	}
	slices.Sort(keys)
	return keys, nil
}

func (d *driver) listEntries(keys []string, key string) []storage.Entry {
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	seenDirs := map[string]struct{}{}
	entries := make([]storage.Entry, 0, len(keys))
	for _, existing := range keys {
		if key != "" && !strings.HasPrefix(existing, prefix) {
			continue
		}
		rest := existing
		if prefix != "" {
			rest = strings.TrimPrefix(existing, prefix)
		}
		parts := strings.Split(rest, "/")
		if len(parts) == 1 {
			entries = append(entries, storage.Entry{
				Path:  d.stripPrefix(existing),
				Size:  0,
				IsDir: false,
			})
			continue
		}
		child := parts[0]
		dirPath := child
		if key != "" {
			dirPath = key + "/" + child
		}
		if _, ok := seenDirs[dirPath]; ok {
			continue
		}
		seenDirs[dirPath] = struct{}{}
		entries = append(entries, storage.Entry{Path: d.stripPrefix(dirPath), IsDir: true})
	}
	slices.SortFunc(entries, func(a, b storage.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries
}

func (d *driver) walkEntries(ctx context.Context, keys []string, key string) ([]storage.Entry, bool, error) {
	size, err := d.objectSize(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if size >= 0 {
		return []storage.Entry{{Path: d.stripPrefix(key), Size: size, IsDir: false}}, true, nil
	}
	if key != "" && !hasChildren(keys, key) {
		return nil, false, nil
	}

	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	seenDirs := map[string]struct{}{}
	entries := make([]storage.Entry, 0, len(keys))
	for _, existing := range keys {
		if key != "" && !strings.HasPrefix(existing, prefix) {
			continue
		}
		for _, dir := range recursiveParentDirs(d.stripPrefix(existing)) {
			fullDir := storage.JoinPrefix(d.prefix, dir)
			if _, ok := seenDirs[fullDir]; ok {
				continue
			}
			seenDirs[fullDir] = struct{}{}
			entries = append(entries, storage.Entry{Path: dir, IsDir: true})
		}
		size, err := d.objectSize(ctx, existing)
		if err != nil {
			return nil, false, err
		}
		if size < 0 {
			continue
		}
		entries = append(entries, storage.Entry{
			Path:  d.stripPrefix(existing),
			Size:  size,
			IsDir: false,
		})
	}
	slices.SortFunc(entries, func(a, b storage.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, true, nil
}

func hasChildren(keys []string, key string) bool {
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	for _, existing := range keys {
		if key == "" || strings.HasPrefix(existing, prefix) {
			return true
		}
	}
	return false
}

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

func redisNamespace(cfg storage.ResolvedConfig) string {
	base := "goforj:storage:redis"
	if cfg.RedisDB != 0 {
		base += ":db:" + strconv.Itoa(cfg.RedisDB)
	}
	return base
}
