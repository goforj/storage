package redisstorage

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/storage/storagecore"
	"github.com/redis/go-redis/v9"
)

func init() {
	storagecore.RegisterDriver("redis", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
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

func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
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
		return nil, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
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
	d.indexPut(pipe, key)
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
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	if err := d.unindexDelete(ctx, key); err != nil {
		return fmt.Errorf("storage: redis delete: %w", err)
	}
	return nil
}

func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return storagecore.Entry{}, err
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

func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
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
	if key != "" && len(entries) == 0 {
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

func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
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
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	pipe := d.client.TxPipeline()
	pipe.HSet(ctx, d.objectKey(dstKey), map[string]any{
		"data":    data,
		"modtime": time.Now().UTC().UnixNano(),
	})
	d.indexPut(pipe, dstKey)
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
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	pipe := d.client.TxPipeline()
	pipe.HSet(ctx, d.objectKey(dstKey), map[string]any{
		"data":    data,
		"modtime": time.Now().UTC().UnixNano(),
	})
	d.indexPut(pipe, dstKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("storage: redis move: %w", err)
	}
	if err := d.DeleteContext(ctx, src); err != nil {
		return err
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
	return "", fmt.Errorf("%w: public URL not supported for redis", storagecore.ErrUnsupported)
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

func (d *driver) Close() error {
	return d.client.Close()
}

func (d *driver) key(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) objectKey(key string) string {
	return d.namespace + ":obj:" + key
}

func (d *driver) dirChildrenKey(key string) string {
	return d.namespace + ":dir:" + key + ":children"
}

func (d *driver) dirObjectsKey(key string) string {
	return d.namespace + ":dir:" + key + ":objects"
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

func (d *driver) children(ctx context.Context, key string) ([]string, error) {
	children, err := d.client.SMembers(ctx, d.dirChildrenKey(key)).Result()
	if err != nil {
		return nil, fmt.Errorf("storage: redis list: %w", err)
	}
	slices.Sort(children)
	return children, nil
}

func (d *driver) listEntries(ctx context.Context, children []string) ([]storagecore.Entry, error) {
	entries := make([]storagecore.Entry, 0, len(children))
	for _, child := range children {
		isDir, key, err := parseChildEntry(child)
		if err != nil {
			return nil, err
		}
		entry := storagecore.Entry{Path: d.stripPrefix(key), IsDir: isDir}
		if !isDir {
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

func (d *driver) walkEntries(ctx context.Context, keys []string, key string) ([]storagecore.Entry, bool, error) {
	size, err := d.objectSize(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if size >= 0 {
		return []storagecore.Entry{{Path: d.stripPrefix(key), Size: size, IsDir: false}}, true, nil
	}
	if key != "" && len(keys) == 0 {
		return nil, false, nil
	}

	seenDirs := map[string]struct{}{}
	entries := make([]storagecore.Entry, 0, len(keys))
	for _, existing := range keys {
		for _, dir := range recursiveParentDirs(d.stripPrefix(existing)) {
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
		entries = append(entries, storagecore.Entry{
			Path:  d.stripPrefix(existing),
			Size:  size,
			IsDir: false,
		})
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, true, nil
}

func (d *driver) descendants(ctx context.Context, key string) ([]string, error) {
	keys, err := d.client.SMembers(ctx, d.dirObjectsKey(key)).Result()
	if err != nil {
		return nil, fmt.Errorf("storage: redis walk: %w", err)
	}
	slices.Sort(keys)
	return keys, nil
}

func (d *driver) dirExists(ctx context.Context, key string) (bool, error) {
	count, err := d.client.SCard(ctx, d.dirObjectsKey(key)).Result()
	if err != nil {
		return false, fmt.Errorf("storage: redis stat: %w", err)
	}
	return count > 0, nil
}

func (d *driver) indexPut(pipe redis.Pipeliner, key string) {
	dirs := objectDirs(key)
	pipe.SAdd(context.Background(), d.dirObjectsKey(""), key)
	if len(dirs) == 0 {
		pipe.SAdd(context.Background(), d.dirChildrenKey(""), encodeFileChild(key))
		return
	}
	for _, dir := range dirs {
		pipe.SAdd(context.Background(), d.dirObjectsKey(dir), key)
	}
	pipe.SAdd(context.Background(), d.dirChildrenKey(""), encodeDirChild(dirs[0]))
	for i := 0; i < len(dirs)-1; i++ {
		pipe.SAdd(context.Background(), d.dirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1]))
	}
	pipe.SAdd(context.Background(), d.dirChildrenKey(dirs[len(dirs)-1]), encodeFileChild(key))
}

func (d *driver) unindexDelete(ctx context.Context, key string) error {
	dirs := objectDirs(key)
	pipe := d.client.TxPipeline()
	pipe.SRem(ctx, d.dirObjectsKey(""), key)
	pipe.SRem(ctx, d.dirChildrenKey(parentDir(key)), encodeFileChild(key))
	for _, dir := range dirs {
		pipe.SRem(ctx, d.dirObjectsKey(dir), key)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		count, err := d.client.SCard(ctx, d.dirObjectsKey(dir)).Result()
		if err != nil {
			return err
		}
		if count > 0 {
			break
		}
		parent := parentDir(dir)
		pipe := d.client.TxPipeline()
		pipe.Del(ctx, d.dirObjectsKey(dir))
		pipe.Del(ctx, d.dirChildrenKey(dir))
		pipe.SRem(ctx, d.dirChildrenKey(parent), encodeDirChild(dir))
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
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

func encodeFileChild(key string) string {
	return "f:" + key
}

func encodeDirChild(key string) string {
	return "d:" + key
}

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

func redisNamespace(cfg storagecore.ResolvedConfig) string {
	base := "goforj:storage:redis"
	if cfg.RedisDB != 0 {
		base += ":db:" + strconv.Itoa(cfg.RedisDB)
	}
	return base
}
