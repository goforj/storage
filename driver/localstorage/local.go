package localstorage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/goforj/storage/storagecore"
)

// init registers package integration before callers construct storage.
func init() {
	storagecore.RegisterDriver("local", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	root   string
	prefix string
}

// Config defines local storage rooted at a filesystem path.
// @group Driver Config
//
// Example: define local storage config
//
//	cfg := localstorage.Config{
//		Root:   "/tmp/storage-local",
//		Prefix: "sandbox",
//	}
//	_ = cfg
//
// Example: define local storage config with all fields
//
//	cfg := localstorage.Config{
//		Root:   "/tmp/storage-local",
//		Prefix: "sandbox", // default: ""
//	}
//	_ = cfg
type Config struct {
	Root   string
	Prefix string
}

// DriverName returns the registry name for local storage.
func (Config) DriverName() string { return "local" }

// ResolvedConfig translates Config into the shared driver boundary.
func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
		Driver: "local",
		Remote: c.Root,
		Prefix: c.Prefix,
	}
}

// New constructs local storage rooted at cfg.Root with an optional prefix.
// @group Driver Constructors
//
// Example: local storage
//
//	fs, _ := localstorage.New(localstorage.Config{
//		Root:   "/tmp/storage-local",
//		Prefix: "sandbox",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

// NewContext constructs local storage and honors cancellation during setup.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig validates the namespace before filesystem access so every
// later operation can use os.Root with a canonical root and logical prefix.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Remote == "" {
		return nil, fmt.Errorf("storage: local storage requires root path")
	}
	cleanPrefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(cfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve local root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, wrapLocalError(fmt.Errorf("create local root: %w", err))
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, wrapLocalError(fmt.Errorf("open local root: %w", err))
	}
	if cleanPrefix != "" {
		if err := rootedMkdirAll(rootHandle, cleanPrefix, 0o755); err != nil {
			return nil, joinCleanup(wrapLocalError(fmt.Errorf("create local prefix: %w", err)), wrapLocalError(rootHandle.Close()))
		}
	}
	if err := rootHandle.Close(); err != nil {
		return nil, wrapLocalError(fmt.Errorf("close local root: %w", err))
	}
	return &driver{root: root, prefix: cleanPrefix}, nil
}

// Get reads one traversal-confined local file without caller cancellation.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext reads through os.Root so symlink swaps cannot escape the configured root.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := d.logicalName(p)
	if err != nil {
		return nil, err
	}
	root, err := d.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, wrapLocalError(err)
	}
	var data bytes.Buffer
	readErr := copyContext(ctx, &data, file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, joinCleanup(wrapLocalError(readErr), wrapLocalError(closeErr))
	}
	if closeErr != nil {
		return nil, wrapLocalError(closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

// Put atomically stores one local file without caller cancellation.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// PutContext atomically replaces a file after syncing its complete contents.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := d.logicalObjectName(p)
	if err != nil {
		return err
	}
	root, err := d.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := rootedMkdirAll(root, path.Dir(name), 0o755); err != nil {
		return wrapLocalError(fmt.Errorf("storage: mkdir %q: %w", path.Dir(name), err))
	}
	mode := existingMode(root, name, 0o644)
	return atomicWrite(ctx, root, name, mode, func(file *os.File) error {
		return writeBytesContext(ctx, file, contents)
	})
}

// MakeDir creates a traversal-confined directory without caller cancellation.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// MakeDirContext creates only paths that remain beneath the configured root.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := d.logicalName(p)
	if err != nil {
		return err
	}
	logicalPath, err := d.displayName(name)
	if err != nil {
		return err
	}
	if logicalPath == "" {
		return nil
	}
	root, err := d.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := rootedMkdirAll(root, name, 0o755); err != nil {
		return wrapLocalError(err)
	}
	return nil
}

// Delete removes one local file or empty directory without caller cancellation.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext removes one file or empty directory without following an escaping symlink.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := d.logicalObjectName(p)
	if err != nil {
		return err
	}
	root, err := d.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(name); err != nil {
		return wrapLocalError(err)
	}
	return nil
}

// Stat inspects one logical local path without caller cancellation.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext reports paths relative to the configured logical namespace.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	name, err := d.logicalName(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	root, err := d.openRoot()
	if err != nil {
		return storagecore.Entry{}, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return storagecore.Entry{}, wrapLocalError(err)
	}
	logicalPath, err := d.displayName(name)
	if err != nil {
		return storagecore.Entry{}, err
	}
	size := info.Size()
	if info.IsDir() {
		size = 0
	}
	return storagecore.Entry{Path: logicalPath, Size: size, IsDir: info.IsDir()}, nil
}

// Exists reports local file presence without treating directories as objects.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext reports only files because directory presence is exposed through Stat.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	name, err := d.logicalName(p)
	if err != nil {
		return false, err
	}
	root, err := d.openRoot()
	if err != nil {
		return false, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		if errorsIsNotExist(err) {
			return false, nil
		}
		return false, wrapLocalError(err)
	}
	return !info.IsDir(), nil
}

// List returns sorted immediate local children without caller cancellation.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage paginates sorted immediate local children without caller cancellation.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext returns immediate children in deterministic path order.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := d.logicalName(p)
	if err != nil {
		return nil, err
	}
	root, err := d.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return nil, wrapLocalError(err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: path %q is not a directory", storagecore.ErrNotFound, p)
	}
	children, err := fs.ReadDir(root.FS(), name)
	if err != nil {
		return nil, wrapLocalError(err)
	}
	base, err := d.displayName(name)
	if err != nil {
		return nil, err
	}
	result := make([]storagecore.Entry, 0, len(children))
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		childName := path.Join(name, child.Name())
		childInfo, err := root.Stat(childName)
		if err != nil {
			return nil, wrapLocalError(err)
		}
		size := childInfo.Size()
		if childInfo.IsDir() {
			size = 0
		}
		result = append(result, storagecore.Entry{
			Path:  path.Join(base, child.Name()),
			Size:  size,
			IsDir: childInfo.IsDir(),
		})
	}
	slices.SortFunc(result, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return result, nil
}

// ListPageContext paginates the same deterministic snapshot returned by ListContext.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

// Walk traverses a confined local subtree without caller cancellation.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext traverses lexicographically through os.Root's restricted filesystem view.
func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("%w: walk callback is required", storagecore.ErrForbidden)
	}
	name, err := d.logicalName(p)
	if err != nil {
		return err
	}
	root, err := d.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return wrapLocalError(err)
	}
	if !info.IsDir() {
		logicalPath, err := d.displayName(name)
		if err != nil {
			return err
		}
		return fn(storagecore.Entry{Path: logicalPath, Size: info.Size()})
	}
	return fs.WalkDir(root.FS(), name, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return wrapLocalError(walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == name {
			return nil
		}
		logicalPath, err := d.displayName(current)
		if err != nil {
			return err
		}
		size := int64(0)
		if !entry.IsDir() {
			entryInfo, err := entry.Info()
			if err != nil {
				return wrapLocalError(err)
			}
			size = entryInfo.Size()
		}
		return fn(storagecore.Entry{Path: logicalPath, Size: size, IsDir: entry.IsDir()})
	})
}

// Copy atomically duplicates one local file without caller cancellation.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext installs a fully written destination atomically so failures preserve prior data.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	srcName, err := d.logicalName(src)
	if err != nil {
		return err
	}
	dstName, err := d.logicalObjectName(dst)
	if err != nil {
		return err
	}
	root, err := d.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	srcInfo, err := root.Stat(srcName)
	if err != nil {
		return wrapLocalError(err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("%w: copy of directory not supported", storagecore.ErrUnsupported)
	}
	if srcName == dstName {
		return nil
	}
	if err := rootedMkdirAll(root, path.Dir(dstName), 0o755); err != nil {
		return wrapLocalError(fmt.Errorf("storage: mkdir %q: %w", path.Dir(dstName), err))
	}
	source, err := root.Open(srcName)
	if err != nil {
		return wrapLocalError(err)
	}
	writeErr := atomicWrite(ctx, root, dstName, existingMode(root, dstName, srcInfo.Mode().Perm()), func(file *os.File) error {
		return copyContext(ctx, file, source)
	})
	closeErr := source.Close()
	if writeErr != nil {
		return joinCleanup(writeErr, wrapLocalError(closeErr))
	}
	return wrapLocalError(closeErr)
}

// Move renames a confined local path without caller cancellation.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext uses rooted atomic replacement so each platform retains os.Root's guarantees.
func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	srcName, err := d.logicalObjectName(src)
	if err != nil {
		return err
	}
	dstName, err := d.logicalObjectName(dst)
	if err != nil {
		return err
	}
	root, err := d.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Stat(srcName); err != nil {
		return wrapLocalError(err)
	}
	if srcName == dstName {
		return nil
	}
	if err := rootedMkdirAll(root, path.Dir(dstName), 0o755); err != nil {
		return wrapLocalError(fmt.Errorf("storage: mkdir %q: %w", path.Dir(dstName), err))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rootedRename(root, srcName, dstName); err != nil {
		return wrapLocalError(err)
	}
	return nil
}

// URL reports local public-link generation as unsupported.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext rejects URL generation after honoring caller cancellation.
func (d *driver) URLContext(ctx context.Context, _ string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%w: public URL not supported for local driver", storagecore.ErrUnsupported)
}

// modTime exposes stable UTC modification times to the shared contract tests.
func (d *driver) modTime(ctx context.Context, p string) (time.Time, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	name, err := d.logicalName(p)
	if err != nil {
		return time.Time{}, err
	}
	root, err := d.openRoot()
	if err != nil {
		return time.Time{}, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return time.Time{}, wrapLocalError(err)
	}
	return info.ModTime().UTC(), nil
}

// resolvePath retains a concrete-path test hook while operations themselves use os.Root.
func (d *driver) resolvePath(p string) (string, error) {
	return d.fullPath(p)
}

// fullPath resolves a logical name lexically for diagnostics and tests only.
func (d *driver) fullPath(p string) (string, error) {
	name, err := d.logicalName(p)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(d.root, filepath.FromSlash(name))
	rel, err := filepath.Rel(d.root, joined)
	if err != nil {
		return "", fmt.Errorf("storage: compute relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes root", storagecore.ErrForbidden)
	}
	return joined, nil
}

// userRelative maps an absolute test path through the same strict prefix boundary as operations.
func (d *driver) userRelative(target string) (string, error) {
	rel, err := filepath.Rel(d.root, target)
	if err != nil {
		return "", fmt.Errorf("storage: compute relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path is outside local root", storagecore.ErrForbidden)
	}
	return d.displayName(filepath.ToSlash(rel))
}

// logicalName joins a normalized user path to the configured namespace prefix.
func (d *driver) logicalName(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	name := storagecore.JoinPrefix(d.prefix, normalized)
	if name == "" {
		return ".", nil
	}
	return name, nil
}

// logicalObjectName rejects the logical namespace root before an operation can
// create, remove, or rename the configured prefix itself.
func (d *driver) logicalObjectName(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", fmt.Errorf("%w: logical root cannot be mutated", storagecore.ErrForbidden)
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// displayName strips only an exact prefix component, preventing partial-prefix leaks.
func (d *driver) displayName(name string) (string, error) {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if name == "." {
		name = ""
	}
	if d.prefix == "" {
		return name, nil
	}
	if name == d.prefix {
		return "", nil
	}
	if !strings.HasPrefix(name, d.prefix+"/") {
		return "", fmt.Errorf("%w: path %q is outside storage prefix", storagecore.ErrForbidden, name)
	}
	return strings.TrimPrefix(name, d.prefix+"/"), nil
}

// openRoot opens a fresh capability for each operation because Storage has no Close contract.
func (d *driver) openRoot() (*os.Root, error) {
	root, err := os.OpenRoot(d.root)
	if err != nil {
		return nil, wrapLocalError(fmt.Errorf("storage: open local root: %w", err))
	}
	return root, nil
}

// atomicWrite writes, syncs, and closes a unique temporary file before replacing the target.
func atomicWrite(ctx context.Context, root *os.Root, target string, mode fs.FileMode, write func(*os.File) error) error {
	temporary, file, err := createTemporary(root, target, mode)
	if err != nil {
		return wrapLocalError(err)
	}
	if err := write(file); err != nil {
		return discardTemporary(root, temporary, file, wrapLocalError(err))
	}
	if err := ctx.Err(); err != nil {
		return discardTemporary(root, temporary, file, err)
	}
	if err := file.Sync(); err != nil {
		return discardTemporary(root, temporary, file, wrapLocalError(err))
	}
	if err := file.Close(); err != nil {
		return discardTemporary(root, temporary, nil, wrapLocalError(err))
	}
	if err := ctx.Err(); err != nil {
		return discardTemporary(root, temporary, nil, err)
	}
	if err := rootedRename(root, temporary, target); err != nil {
		return discardTemporary(root, temporary, nil, wrapLocalError(err))
	}
	return nil
}

// discardTemporary reports cleanup failures without obscuring the operation that triggered cleanup.
func discardTemporary(root *os.Root, temporary string, file *os.File, primary error) error {
	var cleanupErr error
	if file != nil {
		cleanupErr = wrapLocalError(file.Close())
	}
	removeErr := root.Remove(temporary)
	if removeErr != nil && !errorsIsNotExist(removeErr) {
		cleanupErr = joinCleanup(cleanupErr, wrapLocalError(removeErr))
	}
	return joinCleanup(primary, cleanupErr)
}

// joinCleanup preserves the primary error exactly when resource cleanup succeeds.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// createTemporary uses O_EXCL so concurrent writers can never share partial state.
func createTemporary(root *os.Root, target string, mode fs.FileMode) (string, *os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return "", nil, fmt.Errorf("storage: create random temporary name: %w", err)
		}
		name := path.Join(path.Dir(target), "."+path.Base(target)+".storage-"+hex.EncodeToString(random)+".tmp")
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("storage: create temporary file: %w", err)
		}
	}
	return "", nil, fmt.Errorf("storage: create temporary file: exhausted unique names")
}

// existingMode preserves permissions when atomic replacement overwrites a file.
func existingMode(root *os.Root, target string, fallback fs.FileMode) fs.FileMode {
	info, err := root.Stat(target)
	if err != nil || info.IsDir() {
		return fallback
	}
	return info.Mode().Perm()
}

// writeBytesContext checks cancellation between writes so no canceled content is installed.
func writeBytesContext(ctx context.Context, file *os.File, contents []byte) error {
	for len(contents) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		written, err := file.Write(contents)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		contents = contents[written:]
	}
	return nil
}

// copyContext checks cancellation between bounded reads while preserving I/O errors.
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

// wrapLocalError preserves platform errors while exposing portable storage identities.
func wrapLocalError(err error) error {
	if err == nil {
		return nil
	}
	if errorsIsNotExist(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	}
	if errorsIsPermission(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	if isPathEscape(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	return err
}

// isPathEscape recognizes os.Root's currently unexported traversal error.
func isPathEscape(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr) && pathErr.Err != nil && pathErr.Err.Error() == "path escapes from parent"
}

// errorsIsNotExist recognizes portable and OS-specific missing-path errors.
func errorsIsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err)
}

// errorsIsPermission recognizes portable and OS-specific permission errors.
func errorsIsPermission(err error) bool {
	return errors.Is(err, fs.ErrPermission) || os.IsPermission(err)
}
