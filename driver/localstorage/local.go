package localstorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goforj/storage/storagecore"
)

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

func (Config) DriverName() string { return "local" }

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

func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(_ context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
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

	return &driver{
		root:   root,
		prefix: cleanPrefix,
	}, nil
}

func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, wrapLocalError(err)
	}
	return data, nil
}

func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := d.fullPath(p)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("storage: mkdir: %w", err)
	}
	if err := os.WriteFile(target, contents, 0o644); err != nil {
		return wrapLocalError(err)
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
	target, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		return wrapLocalError(err)
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
	target, err := d.fullPath(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return storagecore.Entry{}, wrapLocalError(err)
	}
	rel, err := d.userRelative(target)
	if err != nil {
		return storagecore.Entry{}, err
	}
	size := info.Size()
	if info.IsDir() {
		size = 0
	}
	return storagecore.Entry{Path: rel, Size: size, IsDir: info.IsDir()}, nil
}

func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	target, err := d.fullPath(p)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if errorsIsNotExist(err) {
			return false, nil
		}
		return false, wrapLocalError(err)
	}
	if info.IsDir() {
		return false, nil
	}
	return true, nil
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
	target, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, wrapLocalError(err)
	}

	basePrefix, err := d.userRelative(target)
	if err != nil {
		return nil, err
	}

	var result []storagecore.Entry
	for _, e := range entries {
		name := e.Name()
		rel := filepath.ToSlash(filepath.Join(basePrefix, name))
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		result = append(result, storagecore.Entry{
			Path:  rel,
			Size:  size,
			IsDir: e.IsDir(),
		})
	}
	return result, nil
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
	target, err := d.fullPath(p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return storagecore.ListPageResult{}, wrapLocalError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return storagecore.ListPageResult{}, wrapLocalError(err)
	}
	if !info.IsDir() {
		return storagecore.ListPageResult{}, fmt.Errorf("%w: path is not a directory", storagecore.ErrNotFound)
	}

	basePrefix, err := d.userRelative(target)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}

	result := storagecore.ListPageResult{
		Entries: make([]storagecore.Entry, 0, limit),
		Offset:  offset,
		Limit:   limit,
	}
	skipped := 0
	chunkSize := limit
	if chunkSize < 64 {
		chunkSize = 64
	}

	for {
		batch, readErr := file.ReadDir(chunkSize)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return storagecore.ListPageResult{}, wrapLocalError(readErr)
		}
		if len(batch) == 0 {
			result.HasMore = false
			return result, nil
		}

		for index, entry := range batch {
			if skipped < offset {
				skipped++
				continue
			}

			name := entry.Name()
			rel := filepath.ToSlash(filepath.Join(basePrefix, name))
			info, _ := entry.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			result.Entries = append(result.Entries, storagecore.Entry{
				Path:  rel,
				Size:  size,
				IsDir: entry.IsDir(),
			})
			if len(result.Entries) == limit {
				result.HasMore = index < len(batch)-1
				if !result.HasMore {
					peek, peekErr := file.ReadDir(1)
					if peekErr != nil && !errors.Is(peekErr, io.EOF) {
						return storagecore.ListPageResult{}, wrapLocalError(peekErr)
					}
					result.HasMore = len(peek) > 0
				}
				return result, nil
			}
		}

		if errors.Is(readErr, io.EOF) {
			result.HasMore = false
			return result, nil
		}
	}
}

func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := d.fullPath(p)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return wrapLocalError(err)
	}
	if !info.IsDir() {
		rel, err := d.userRelative(target)
		if err != nil {
			return err
		}
		return fn(storagecore.Entry{Path: rel, Size: info.Size(), IsDir: false})
	}

	return filepath.WalkDir(target, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return wrapLocalError(walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == target {
			return nil
		}
		rel, err := d.userRelative(current)
		if err != nil {
			return err
		}
		size := int64(0)
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return wrapLocalError(err)
			}
			size = info.Size()
		}
		return fn(storagecore.Entry{
			Path:  rel,
			Size:  size,
			IsDir: entry.IsDir(),
		})
	})
}

func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	srcTarget, err := d.fullPath(src)
	if err != nil {
		return err
	}
	srcInfo, err := os.Stat(srcTarget)
	if err != nil {
		return wrapLocalError(err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("%w: copy of directory not supported", storagecore.ErrUnsupported)
	}
	dstTarget, err := d.fullPath(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstTarget), 0o755); err != nil {
		return fmt.Errorf("storage: mkdir: %w", err)
	}
	in, err := os.Open(srcTarget)
	if err != nil {
		return wrapLocalError(err)
	}
	defer in.Close()
	out, err := os.Create(dstTarget)
	if err != nil {
		return wrapLocalError(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return wrapLocalError(err)
	}
	return wrapLocalError(out.Close())
}

func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	srcTarget, err := d.fullPath(src)
	if err != nil {
		return err
	}
	srcInfo, err := os.Stat(srcTarget)
	if err != nil {
		return wrapLocalError(err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("%w: move of directory not supported", storagecore.ErrUnsupported)
	}
	dstTarget, err := d.fullPath(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstTarget), 0o755); err != nil {
		return fmt.Errorf("storage: mkdir: %w", err)
	}
	if err := os.Rename(srcTarget, dstTarget); err != nil {
		return wrapLocalError(err)
	}
	return nil
}

func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

func (d *driver) URLContext(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: public URL not supported for local driver", storagecore.ErrUnsupported)
}

// modTime returns the file modification time. This is a test helper and not part of the public API.
func (d *driver) modTime(_ context.Context, p string) (time.Time, error) {
	target, err := d.fullPath(p)
	if err != nil {
		return time.Time{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return time.Time{}, wrapLocalError(err)
	}
	return info.ModTime().UTC(), nil
}

// resolvePath exposes the concrete path for testing prefix isolation.
func (d *driver) resolvePath(p string) (string, error) {
	return d.fullPath(p)
}

func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(d.root, filepath.FromSlash(storagecore.JoinPrefix(d.prefix, normalized)))
	rel, err := filepath.Rel(d.root, joined)
	if err != nil {
		return "", fmt.Errorf("storage: compute relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%w: path escapes root", storagecore.ErrForbidden)
	}
	return joined, nil
}

func (d *driver) userRelative(target string) (string, error) {
	rel, err := filepath.Rel(d.root, target)
	if err != nil {
		return "", fmt.Errorf("storage: compute relative path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, d.prefix)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." {
		return "", nil
	}
	return rel, nil
}

func wrapLocalError(err error) error {
	if errorsIsNotExist(err) {
		return fmt.Errorf("%w: %v", storagecore.ErrNotFound, err)
	}
	if errorsIsPermission(err) {
		return fmt.Errorf("%w: %v", storagecore.ErrForbidden, err)
	}
	return err
}

func errorsIsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err)
}

func errorsIsPermission(err error) bool {
	return errors.Is(err, fs.ErrPermission) || os.IsPermission(err)
}
