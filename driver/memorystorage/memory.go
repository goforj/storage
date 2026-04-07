package memorystorage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goforj/storage/storagecore"
)

func init() {
	storagecore.RegisterDriver("memory", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type object struct {
	data    []byte
	modTime time.Time
}

type driver struct {
	mu      sync.RWMutex
	prefix  string
	objects map[string]object
}

// Config defines an in-memory storage disk.
// @group Driver Config
//
// Example: define memory storage config
//
//	cfg := memorystorage.Config{}
//	_ = cfg
//
// Example: define memory storage config with all fields
//
//	cfg := memorystorage.Config{
//		Prefix: "sandbox", // default: ""
//	}
//	_ = cfg
type Config struct {
	Prefix string
}

func (Config) DriverName() string { return "memory" }

func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
		Driver: "memory",
		Prefix: c.Prefix,
	}
}

// New constructs in-memory storage.
// @group Driver Constructors
//
// Example: memory storage
//
//	fs, _ := memorystorage.New(memorystorage.Config{
//		Prefix: "sandbox",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(_ context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	return &driver{
		prefix:  prefix,
		objects: make(map[string]object),
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
	d.mu.RLock()
	obj, ok := d.objects[key]
	d.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	return slices.Clone(obj.data), nil
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
	d.mu.Lock()
	d.objects[key] = object{
		data:    slices.Clone(contents),
		modTime: time.Now().UTC(),
	}
	d.mu.Unlock()
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.objects[key]; !ok {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	delete(d.objects, key)
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
	d.mu.RLock()
	defer d.mu.RUnlock()
	if obj, ok := d.objects[key]; ok {
		return storagecore.Entry{Path: d.stripPrefix(key), Size: int64(len(obj.data)), IsDir: false}, nil
	}
	if d.hasChildrenLocked(key) {
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
	d.mu.RLock()
	_, ok := d.objects[key]
	d.mu.RUnlock()
	return ok, nil
}

func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	entries := d.listEntriesLocked(key)
	if key != "" && len(entries) == 0 {
		if _, ok := d.objects[key]; ok {
			return nil, fmt.Errorf("%w: path is not a directory", storagecore.ErrNotFound)
		}
		if !d.hasChildrenLocked(key) {
			return nil, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
		}
	}
	return entries, nil
}

func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	if err := ctx.Err(); err != nil {
		return storagecore.ListPageResult{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	key, err := d.key(p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	entries := d.listEntriesLocked(key)
	if key != "" && len(entries) == 0 {
		if _, ok := d.objects[key]; ok {
			return storagecore.ListPageResult{}, fmt.Errorf("%w: path is not a directory", storagecore.ErrNotFound)
		}
		if !d.hasChildrenLocked(key) {
			return storagecore.ListPageResult{}, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
		}
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	pageEntries := make([]storagecore.Entry, end-offset)
	copy(pageEntries, entries[offset:end])
	return storagecore.ListPageResult{
		Entries: pageEntries,
		Offset:  offset,
		Limit:   limit,
		HasMore: end < len(entries),
	}, nil
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
	d.mu.RLock()
	entries, ok := d.walkEntriesLocked(key)
	d.mu.RUnlock()
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
	d.mu.Lock()
	defer d.mu.Unlock()
	obj, ok := d.objects[srcKey]
	if !ok {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	d.objects[dstKey] = object{
		data:    slices.Clone(obj.data),
		modTime: time.Now().UTC(),
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
	d.mu.Lock()
	defer d.mu.Unlock()
	obj, ok := d.objects[srcKey]
	if !ok {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	d.objects[dstKey] = object{
		data:    obj.data,
		modTime: time.Now().UTC(),
	}
	delete(d.objects, srcKey)
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
	return "", fmt.Errorf("%w: public URL not supported for memory", storagecore.ErrUnsupported)
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
	d.mu.RLock()
	obj, ok := d.objects[key]
	d.mu.RUnlock()
	if !ok {
		return time.Time{}, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	return obj.modTime, nil
}

func (d *driver) key(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) stripPrefix(key string) string {
	if d.prefix == "" {
		return key
	}
	trimmed := strings.TrimPrefix(key, d.prefix)
	return strings.TrimPrefix(trimmed, "/")
}

func (d *driver) hasChildrenLocked(key string) bool {
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	for existing := range d.objects {
		if key == "" || strings.HasPrefix(existing, prefix) {
			return true
		}
	}
	return false
}

func (d *driver) listEntriesLocked(key string) []storagecore.Entry {
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	seenDirs := map[string]struct{}{}
	var entries []storagecore.Entry
	for existing, obj := range d.objects {
		if key != "" && !strings.HasPrefix(existing, prefix) {
			continue
		}
		rest := existing
		if prefix != "" {
			rest = strings.TrimPrefix(existing, prefix)
		}
		parts := strings.Split(rest, "/")
		if len(parts) == 1 {
			entries = append(entries, storagecore.Entry{
				Path:  d.stripPrefix(existing),
				Size:  int64(len(obj.data)),
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
		entries = append(entries, storagecore.Entry{
			Path:  d.stripPrefix(dirPath),
			Size:  0,
			IsDir: true,
		})
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries
}

func (d *driver) walkEntriesLocked(key string) ([]storagecore.Entry, bool) {
	if obj, ok := d.objects[key]; ok {
		return []storagecore.Entry{{Path: d.stripPrefix(key), Size: int64(len(obj.data)), IsDir: false}}, true
	}
	if key != "" && !d.hasChildrenLocked(key) {
		return nil, false
	}

	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	seenDirs := map[string]struct{}{}
	var entries []storagecore.Entry
	for existing, obj := range d.objects {
		if key != "" && !strings.HasPrefix(existing, prefix) {
			continue
		}
		for _, dir := range recursiveParentDirs(d.stripPrefix(existing)) {
			fullDir := storagecore.JoinPrefix(d.prefix, dir)
			if _, ok := seenDirs[fullDir]; ok {
				continue
			}
			seenDirs[fullDir] = struct{}{}
			entries = append(entries, storagecore.Entry{Path: dir, IsDir: true})
		}
		entries = append(entries, storagecore.Entry{
			Path:  d.stripPrefix(existing),
			Size:  int64(len(obj.data)),
			IsDir: false,
		})
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, true
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
