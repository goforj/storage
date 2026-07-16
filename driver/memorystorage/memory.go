package memorystorage

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goforj/storage/storagecore"
)

// init registers package integration before callers construct storage.
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
	dirs    map[string]struct{}
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

// DriverName returns the registry identifier for in-memory storage.
func (Config) DriverName() string { return "memory" }

// ResolvedConfig maps the optional memory prefix into storagecore's shared configuration.
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

// NewContext validates cfg and creates an isolated in-memory store.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig normalizes the prefix and initializes empty synchronized indexes.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	return &driver{
		prefix:  prefix,
		dirs:    make(map[string]struct{}),
		objects: make(map[string]object),
	}, nil
}

// Get retrieves an object using a background context.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext returns a clone so callers cannot mutate the stored byte slice.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	obj, ok := d.objects[key]
	if !ok {
		return nil, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	return slices.Clone(obj.data), nil
}

// Put stores an object using a background context.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir creates a directory chain using a background context.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext atomically validates path collisions and stores a private content copy.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.ensureObjectPathLocked(key); err != nil {
		return err
	}
	d.ensureDirChainLocked(key)
	d.objects[key] = object{
		data:    slices.Clone(contents),
		modTime: time.Now().UTC(),
	}
	return nil
}

// MakeDirContext atomically creates an idempotent explicit directory chain.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.ensureDirectoryPathLocked(key); err != nil {
		return err
	}
	d.ensureDirChainLocked(key)
	d.dirs[key] = struct{}{}
	return nil
}

// Delete removes one object or empty directory using a background context.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext removes one entry atomically and rejects non-empty directories.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := d.objects[key]; ok {
		delete(d.objects, key)
		return nil
	}
	if _, ok := d.dirs[key]; ok {
		if d.hasChildrenLocked(key) {
			return fmt.Errorf("%w: directory not empty", storagecore.ErrForbidden)
		}
		delete(d.dirs, key)
		return nil
	}
	return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
}

// Stat inspects a logical path using a background context.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext recognizes concrete objects, explicit directories, and implied parent directories.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	if obj, ok := d.objects[key]; ok {
		return storagecore.Entry{Path: d.stripPrefix(key), Size: int64(len(obj.data)), IsDir: false}, nil
	}
	if _, ok := d.dirs[key]; ok {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	}
	if d.hasChildrenLocked(key) {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	}
	if d.stripPrefix(key) == "" {
		return storagecore.Entry{IsDir: true}, nil
	}
	return storagecore.Entry{}, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
}

// Exists reports object presence using a background context.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext reports only concrete objects, not directory entries.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := d.key(p)
	if err != nil {
		return false, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, ok := d.objects[key]
	return ok, nil
}

// List returns immediate logical children using a background context.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage paginates immediate logical children using a background context.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext returns a sorted one-level snapshot under a read lock.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries := d.listEntriesLocked(key)
	if d.stripPrefix(key) != "" && len(entries) == 0 {
		if _, ok := d.objects[key]; ok {
			return nil, fmt.Errorf("%w: path is not a directory", storagecore.ErrNotFound)
		}
		if !d.hasChildrenLocked(key) {
			return nil, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
		}
	}
	return entries, nil
}

// ListPageContext slices one sorted snapshot while retaining the read lock.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	ctx = normalizeContext(ctx)
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
	if err := ctx.Err(); err != nil {
		return storagecore.ListPageResult{}, err
	}
	entries := d.listEntriesLocked(key)
	if d.stripPrefix(key) != "" && len(entries) == 0 {
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

// Walk traverses a logical subtree using a background context.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext snapshots sorted entries before invoking re-entrant user callbacks.
func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	ctx = normalizeContext(ctx)
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
	d.mu.RLock()
	if err := ctx.Err(); err != nil {
		d.mu.RUnlock()
		return err
	}
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

// Copy duplicates an object using a background context.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext clones source bytes and updates the destination atomically.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
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
	if d.stripPrefix(dstKey) == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	obj, ok := d.objects[srcKey]
	if !ok {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	if srcKey == dstKey {
		return nil
	}
	if err := d.ensureObjectPathLocked(dstKey); err != nil {
		return err
	}
	d.objects[dstKey] = object{
		data:    slices.Clone(obj.data),
		modTime: time.Now().UTC(),
	}
	return nil
}

// Move relocates an object or directory tree using a background context.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext delegates trees to storagecore and atomically rekeys individual objects.
func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	obj, ok := d.objects[srcKey]
	if !ok {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	if err := d.ensureObjectPathLocked(dstKey); err != nil {
		return err
	}
	d.objects[dstKey] = object{
		data:    obj.data,
		modTime: time.Now().UTC(),
	}
	delete(d.objects, srcKey)
	return nil
}

// URL reports in-memory URL generation as unsupported using a background context.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext validates that the path exists before reporting unsupported URL generation.
func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	ctx = normalizeContext(ctx)
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
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return time.Time{}, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	obj, ok := d.objects[key]
	if !ok {
		return time.Time{}, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	return obj.modTime, nil
}

// key normalizes a logical path before joining the configured memory prefix.
func (d *driver) key(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// stripPrefix removes only an exact configured component from an internal key.
func (d *driver) stripPrefix(key string) string {
	if d.prefix == "" {
		return key
	}
	if key == d.prefix {
		return ""
	}
	return strings.TrimPrefix(key, d.prefix+"/")
}

// ensureObjectPathLocked rejects object-directory overlap while allowing an
// existing object at the exact path to be overwritten.
func (d *driver) ensureObjectPathLocked(key string) error {
	if _, ok := d.dirs[key]; ok || d.hasChildrenLocked(key) {
		return fmt.Errorf("%w: destination is a directory", storagecore.ErrForbidden)
	}
	for parent := path.Dir(key); parent != "." && parent != ""; parent = path.Dir(parent) {
		if _, ok := d.objects[parent]; ok {
			return fmt.Errorf("%w: parent path %q is an object", storagecore.ErrForbidden, d.stripPrefix(parent))
		}
	}
	return nil
}

// ensureDirectoryPathLocked keeps MakeDir idempotent for directories but
// rejects any object occupying the requested path or one of its parents.
func (d *driver) ensureDirectoryPathLocked(key string) error {
	for candidate := key; candidate != "." && candidate != ""; candidate = path.Dir(candidate) {
		if _, ok := d.objects[candidate]; ok {
			return fmt.Errorf("%w: path %q is an object", storagecore.ErrForbidden, d.stripPrefix(candidate))
		}
	}
	return nil
}

// hasChildrenLocked reports whether either index contains a descendant of key.
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
	for existing := range d.dirs {
		if key == "" || strings.HasPrefix(existing, prefix) {
			return true
		}
	}
	return false
}

// listEntriesLocked merges concrete and implied immediate children into sorted entries.
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
	for existing := range d.dirs {
		if key != "" && !strings.HasPrefix(existing, prefix) {
			continue
		}
		rest := existing
		if prefix != "" {
			rest = strings.TrimPrefix(existing, prefix)
		}
		if rest == "" {
			continue
		}
		parts := strings.Split(rest, "/")
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

// walkEntriesLocked builds a sorted subtree snapshot while deduplicating implied directories.
func (d *driver) walkEntriesLocked(key string) ([]storagecore.Entry, bool) {
	if obj, ok := d.objects[key]; ok {
		return []storagecore.Entry{{Path: d.stripPrefix(key), Size: int64(len(obj.data)), IsDir: false}}, true
	}
	if _, ok := d.dirs[key]; !ok && d.stripPrefix(key) != "" && !d.hasChildrenLocked(key) {
		return nil, false
	}

	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	seenDirs := map[string]struct{}{}
	var entries []storagecore.Entry
	requested := d.stripPrefix(key)
	for existing, obj := range d.objects {
		if key != "" && !strings.HasPrefix(existing, prefix) {
			continue
		}
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
		entries = append(entries, storagecore.Entry{
			Path:  d.stripPrefix(existing),
			Size:  int64(len(obj.data)),
			IsDir: false,
		})
	}
	for existing := range d.dirs {
		if key != "" && existing != key && !strings.HasPrefix(existing, prefix) {
			continue
		}
		if existing == "" || existing == key {
			continue
		}
		fullDir := existing
		if _, ok := seenDirs[fullDir]; ok {
			continue
		}
		seenDirs[fullDir] = struct{}{}
		entries = append(entries, storagecore.Entry{Path: d.stripPrefix(fullDir), IsDir: true})
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, true
}

// ensureDirChainLocked records every parent required by an object or explicit directory.
func (d *driver) ensureDirChainLocked(key string) {
	dir := path.Dir(key)
	for dir != "." && dir != "" {
		d.dirs[dir] = struct{}{}
		next := path.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
}

// recursiveParentDirs returns implied parents from shallowest to deepest.
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
