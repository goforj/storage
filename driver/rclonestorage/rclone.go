package rclonestorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/walk"

	// Backends (all)
	_ "github.com/rclone/rclone/backend/all"

	"github.com/goforj/storage/storagecore"
)

// init registers package integration before callers construct storage.
func init() {
	storagecore.RegisterDriver("rclone", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	fs        fs.Fs
	prefix    string
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// Config defines an rclone-backed storage disk.
// @group Driver Config
//
// Example: define rclone storage config
//
//	cfg := rclonestorage.Config{
//		Remote: "local:",
//		Prefix: "sandbox",
//	}
//	_ = cfg
//
// Example: define rclone storage config with all fields
//
//	cfg := rclonestorage.Config{
//		Remote:           "local:",
//		Prefix:           "sandbox",                  // default: ""
//		RcloneConfigPath: "/path/to/rclone.conf",     // default: ""
//		RcloneConfigData: "[local]\ntype = local\n",  // default: ""
//	}
//	_ = cfg
type Config struct {
	Remote           string
	Prefix           string
	RcloneConfigPath string
	RcloneConfigData string
}

// DriverName returns the registry identifier for rclone-backed storage.
func (Config) DriverName() string { return "rclone" }

// ResolvedConfig maps rclone remote and configuration settings into storagecore.
func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
		Driver:           "rclone",
		Remote:           c.Remote,
		Prefix:           c.Prefix,
		RcloneConfigPath: c.RcloneConfigPath,
		RcloneConfigData: c.RcloneConfigData,
	}
}

var (
	rcloneConfigMu     sync.Mutex
	rcloneConfigured   bool
	initErr            error
	initConfigKind     string
	initConfigPath     string
	initConfigDataHash [sha256.Size]byte
	setConfigPath      = config.SetConfigPath
	installConfig      = configfile.Install
	newRcloneFS        = fs.NewFs
)

// New constructs an rclone-backed storage. All disks share a single config path.
// @group Driver Constructors
//
// Example: rclone storage
//
//	fs, _ := rclonestorage.New(rclonestorage.Config{
//		Remote: "local:",
//		Prefix: "sandbox",
//	})
//	_ = fs
//
// Example: rclone storage with inline config
//
//	fs, _ := rclonestorage.New(rclonestorage.Config{
//		Remote: "localdisk:/tmp/storage",
//		RcloneConfigData: `
//
// [localdisk]
// type = local
// `,
//
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

// NewContext validates cfg and constructs an rclone-backed store.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig serializes process-global rclone initialization before creating the remote filesystem.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Remote == "" {
		return nil, fmt.Errorf("storage: rclone storage requires remote")
	}

	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	rcloneConfigMu.Lock()
	defer rcloneConfigMu.Unlock()
	if err := initRcloneLocked(cfg); err != nil {
		return nil, err
	}

	rcloneFS, err := newRcloneFS(ctx, cfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("storage: create rclone fs: %w", err)
	}

	return &driver{
		fs:     rcloneFS,
		prefix: prefix,
	}, nil
}

// Get retrieves an object using a background context.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext reads one rclone object and preserves both transfer and close failures.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	if d.stripPrefix(remote) == "" {
		return nil, fmt.Errorf("%w: logical root cannot be read as an object", storagecore.ErrForbidden)
	}
	obj, err := d.fs.NewObject(ctx, remote)
	if err != nil {
		return nil, wrapError(err)
	}
	rc, err := obj.Open(ctx)
	if err != nil {
		return nil, wrapError(err)
	}
	var data bytes.Buffer
	readErr := copyContext(ctx, &data, rc)
	closeErr := rc.Close()
	if readErr != nil {
		return nil, joinCleanup(wrapError(readErr), wrapError(closeErr))
	}
	if closeErr != nil {
		return nil, wrapError(closeErr)
	}
	return data.Bytes(), nil
}

// Put stores an object using a background context.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir creates a directory using a background context.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext creates the destination parent and uploads an object with current modification time.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(remote) == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	if dir := path.Dir(remote); dir != "" && dir != "." {
		if mkErr := d.fs.Mkdir(ctx, dir); mkErr != nil {
			return wrapError(mkErr)
		}
	}

	modTime := time.Now().UTC()
	src := object.NewStaticObjectInfo(remote, modTime, int64(len(contents)), true, nil, nil)
	if _, err := d.fs.Put(ctx, bytes.NewReader(contents), src); err != nil {
		return wrapError(err)
	}
	return nil
}

// MakeDirContext delegates recursive directory creation and treats the logical root as existing.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(remote) == "" {
		return nil
	}
	if err := d.fs.Mkdir(ctx, remote); err != nil {
		return wrapError(err)
	}
	return nil
}

// Delete removes one object or empty directory using a background context.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext stats the path before selecting rclone object or empty-directory removal.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(remote) == "" {
		return fmt.Errorf("%w: cannot delete storage root", storagecore.ErrForbidden)
	}
	entry, err := d.StatContext(ctx, p)
	if err != nil {
		return err
	}
	if entry.IsDir {
		if err := d.fs.Rmdir(ctx, remote); err != nil {
			return wrapError(err)
		}
		return nil
	}
	obj, err := d.fs.NewObject(ctx, remote)
	if err != nil {
		return wrapError(err)
	}
	if err := obj.Remove(ctx); err != nil {
		return wrapError(err)
	}
	return nil
}

// Stat inspects a logical rclone path using a background context.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext reconciles object lookup, directory listing, and parent entries across rclone backends.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return storagecore.Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if d.stripPrefix(remote) == "" {
		return storagecore.Entry{Path: "", IsDir: true}, nil
	}
	obj, err := d.fs.NewObject(ctx, remote)
	if err == nil {
		return storagecore.Entry{Path: d.stripPrefix(remote), Size: obj.Size(), IsDir: false}, nil
	}
	if errors.Is(err, fs.ErrorIsDir) {
		return storagecore.Entry{Path: d.stripPrefix(remote), IsDir: true}, nil
	}
	if !isNotFound(err) {
		return storagecore.Entry{}, wrapError(err)
	}
	if _, listErr := d.fs.List(ctx, remote); listErr == nil {
		return storagecore.Entry{Path: d.stripPrefix(remote), IsDir: true}, nil
	} else if !isNotFound(listErr) {
		return storagecore.Entry{}, wrapError(listErr)
	}

	parent := path.Dir(remote)
	if parent == "." {
		parent = ""
	}
	entries, listErr := d.fs.List(ctx, parent)
	if listErr != nil {
		return storagecore.Entry{}, wrapError(listErr)
	}
	for _, entry := range entries {
		if entry.Remote() != remote {
			continue
		}
		if _, ok := entry.(fs.Directory); ok {
			return storagecore.Entry{Path: d.stripPrefix(remote), IsDir: true}, nil
		}
		return storagecore.Entry{Path: d.stripPrefix(remote), Size: entry.Size(), IsDir: false}, nil
	}
	return storagecore.Entry{}, fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
}

// Exists reports object presence using a background context.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext probes concrete objects without reporting directories as files.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return false, err
	}
	if d.stripPrefix(remote) == "" {
		return false, nil
	}
	_, err = d.fs.NewObject(ctx, remote)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, wrapError(err)
	}
	return true, nil
}

// List returns immediate rclone children using a background context.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage paginates immediate rclone children using a background context.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext converts rclone entries to sorted logical paths and zeroes directory sizes.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	entries, err := d.fs.List(ctx, remote)
	if err != nil {
		if d.stripPrefix(remote) == "" && isNotFound(err) {
			return []storagecore.Entry{}, nil
		}
		return nil, wrapError(err)
	}

	var result []storagecore.Entry
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel := d.stripPrefix(entry.Remote())
		if rel == "" {
			continue
		}

		isDir := false
		size := entry.Size()
		if _, ok := entry.(fs.Directory); ok {
			isDir = true
			size = 0
		}

		result = append(result, storagecore.Entry{
			Path:  rel,
			Size:  size,
			IsDir: isDir,
		})
	}
	slices.SortFunc(result, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return result, nil
}

// ListPageContext paginates the deterministic snapshot returned by ListContext.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

// Walk traverses a logical rclone subtree using a background context.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext snapshots recursive rclone entries before invoking sorted user callbacks.
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
	remote, err := d.fullPath(p)
	if err != nil {
		return err
	}
	rootEntry, err := d.StatContext(ctx, p)
	if err != nil {
		return err
	}
	if !rootEntry.IsDir {
		return fn(rootEntry)
	}
	var result []storagecore.Entry
	err = walk.ListR(ctx, d.fs, remote, true, -1, walk.ListAll, func(entries fs.DirEntries) error {
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := d.stripPrefix(entry.Remote())
			if rel == "" || rel == d.stripPrefix(remote) {
				continue
			}
			isDir := false
			size := entry.Size()
			if _, ok := entry.(fs.Directory); ok {
				isDir = true
				size = 0
			}
			result = append(result, storagecore.Entry{Path: rel, Size: size, IsDir: isDir})
		}
		return nil
	})
	if err != nil {
		if d.stripPrefix(remote) == "" && isNotFound(err) {
			return nil
		}
		return wrapError(err)
	}
	slices.SortFunc(result, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, entry := range result {
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

// CopyContext validates both paths and preserves a normalized same-path object unchanged.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
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
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	if srcPath == dstPath {
		return nil
	}
	return d.PutContext(ctx, dst, data)
}

// Move relocates an object or directory tree using a background context.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext delegates trees to storagecore and otherwise copies before deleting.
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
	if err := d.CopyContext(ctx, src, dst); err != nil {
		return err
	}
	return d.DeleteContext(ctx, src)
}

// URL requests a public object link using a background context.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext uses the backend's PublicLink feature and preserves unsupported distinctions.
func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return "", err
	}
	if d.stripPrefix(remote) == "" {
		return "", fmt.Errorf("%w: logical root does not have an object URL", storagecore.ErrForbidden)
	}
	features := d.fs.Features()
	if features == nil || features.PublicLink == nil {
		return "", storagecore.ErrUnsupported
	}
	url, err := operations.PublicLink(ctx, d.fs, remote, 0, false)
	if err != nil {
		if isNotFound(err) || errors.Is(err, fs.ErrorPermissionDenied) {
			return "", wrapError(err)
		}
		if errors.Is(err, fs.ErrorCantShareDirectories) || errors.Is(err, fs.ErrorNotImplemented) {
			return "", fmt.Errorf("%w: %w", storagecore.ErrUnsupported, err)
		}
		return "", err
	}
	return url, nil
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
	remote, err := d.fullPath(p)
	if err != nil {
		return time.Time{}, err
	}
	if d.stripPrefix(remote) == "" {
		return time.Time{}, fmt.Errorf("%w: logical root has no object modification time", storagecore.ErrForbidden)
	}
	obj, err := d.fs.NewObject(ctx, remote)
	if err != nil {
		return time.Time{}, wrapError(err)
	}
	return obj.ModTime(ctx).UTC(), nil
}

// initRclone serializes process-global rclone configuration changes.
func initRclone(cfg storagecore.ResolvedConfig) error {
	rcloneConfigMu.Lock()
	defer rcloneConfigMu.Unlock()
	return initRcloneLocked(cfg)
}

// initRcloneLocked installs one immutable process-wide config identity and rejects later conflicts.
func initRcloneLocked(cfg storagecore.ResolvedConfig) error {
	if initErr != nil {
		return initErr
	}
	if cfg.RcloneConfigData != "" && cfg.RcloneConfigPath != "" {
		return fmt.Errorf("storage: only one of RcloneConfigPath or RcloneConfigData may be set")
	}

	kind := "default"
	identity := config.GetConfigPath()
	dataHash := [sha256.Size]byte{}
	if cfg.RcloneConfigData != "" {
		kind = "inline"
		dataHash = sha256.Sum256([]byte(cfg.RcloneConfigData))
		identity = fmt.Sprintf("%x", dataHash)
	} else if cfg.RcloneConfigPath != "" {
		kind = "path"
		identity = cfg.RcloneConfigPath
	}

	if rcloneConfigured {
		if initErr != nil {
			return initErr
		}
		same := kind == initConfigKind
		if same && kind == "default" {
			same = identity == initConfigPath
		}
		if same && kind == "path" {
			same = identity == initConfigPath
		}
		if same && kind == "inline" {
			same = dataHash == initConfigDataHash
		}
		if !same {
			return fmt.Errorf("storage: rclone is already initialized with a different process-global config")
		}
		return nil
	}

	rcloneConfigured = true
	initConfigKind = kind
	if kind == "inline" {
		storage, err := newMemoryStorage(cfg.RcloneConfigData)
		if err != nil {
			initErr = err
			return initErr
		}
		initConfigPath = "inline-rclone.conf"
		initConfigDataHash = dataHash
		if err := setConfigPath(initConfigPath); err != nil {
			initErr = err
			return initErr
		}
		config.SetData(storage)
		return nil
	}
	if kind == "path" {
		initConfigPath = identity
		if err := setConfigPath(initConfigPath); err != nil {
			initErr = err
			return initErr
		}
		installConfig()
		return nil
	}
	initConfigPath = identity
	return nil
}

// Close releases backend-owned connections and workers once using a bounded cleanup context.
func (d *driver) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		features := d.fs.Features()
		if features == nil || features.Shutdown == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		d.closeErr = features.Shutdown(ctx)
	})
	return d.closeErr
}

// closedError prevents work from starting after this driver's backend has been released.
func (d *driver) closedError() error {
	if d.closed.Load() {
		return fmt.Errorf("storage: rclone: %w", stdfs.ErrClosed)
	}
	return nil
}

// fullPath normalizes a logical path before joining the configured rclone prefix.
func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// stripPrefix removes only an exact configured component from a remote path.
func (d *driver) stripPrefix(remote string) string {
	if d.prefix == "" {
		return remote
	}
	if remote == d.prefix {
		return ""
	}
	return strings.TrimPrefix(remote, d.prefix+"/")
}

// wrapError maps rclone sentinel failures to portable storage error identities.
func wrapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case isNotFound(err):
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	case errors.Is(err, fs.ErrorPermissionDenied):
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	case errors.Is(err, fs.ErrorDirectoryNotEmpty) || errors.Is(err, syscall.ENOTEMPTY) || strings.Contains(strings.ToLower(err.Error()), "directory not empty"):
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	case errors.Is(err, hash.ErrUnsupported):
		return fmt.Errorf("%w: %w", storagecore.ErrUnsupported, err)
	}
	return err
}

// copyContext transfers bytes while checking cancellation between read cycles.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			if writeErr != nil {
				return writeErr
			}
			if written != read {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// joinCleanup preserves a primary transfer failure while retaining cleanup errors.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// isNotFound recognizes rclone's object, directory, and not-a-file absence sentinels.
func isNotFound(err error) bool {
	return errors.Is(err, fs.ErrorObjectNotFound) ||
		errors.Is(err, fs.ErrorDirNotFound) ||
		errors.Is(err, fs.ErrorNotAFile)
}
