package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goforj/storage"
)

func init() {
	storage.RegisterDriver("local", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type Driver struct {
	root   string
	prefix string
}

type Config struct {
	Remote string
	Prefix string
}

func (Config) DriverName() string { return "local" }

func (c Config) ResolvedConfig() storage.ResolvedConfig {
	return storage.ResolvedConfig{
		Driver: "local",
		Remote: c.Remote,
		Prefix: c.Prefix,
	}
}

// New constructs a local driver rooted at cfg.Remote with an optional prefix.
// @group Drivers
//
// Example: local driver
//
//	fs, _ := local.New(context.Background(), local.Config{Remote: "/tmp", Prefix: "sandbox"})
func New(ctx context.Context, cfg Config) (storage.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(_ context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	if cfg.Remote == "" {
		return nil, fmt.Errorf("storage: local driver requires remote path")
	}
	cleanPrefix, err := storage.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	root, err := filepath.Abs(cfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve local root: %w", err)
	}

	return &Driver{
		root:   root,
		prefix: cleanPrefix,
	}, nil
}

func (d *Driver) Get(ctx context.Context, p string) ([]byte, error) {
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

func (d *Driver) Put(ctx context.Context, p string, contents []byte) error {
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

func (d *Driver) Delete(ctx context.Context, p string) error {
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

func (d *Driver) Exists(ctx context.Context, p string) (bool, error) {
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

func (d *Driver) List(ctx context.Context, p string) ([]storage.Entry, error) {
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

	var result []storage.Entry
	for _, e := range entries {
		name := e.Name()
		rel := filepath.ToSlash(filepath.Join(basePrefix, name))
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		result = append(result, storage.Entry{
			Path:  rel,
			Size:  size,
			IsDir: e.IsDir(),
		})
	}
	return result, nil
}

func (d *Driver) URL(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: public URL not supported for local driver", storage.ErrUnsupported)
}

// ModTime returns the file modification time. This is a test helper and not part of the public API.
func (d *Driver) ModTime(_ context.Context, p string) (time.Time, error) {
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

// ResolvePath exposes the concrete path for testing prefix isolation.
func (d *Driver) ResolvePath(p string) (string, error) {
	return d.fullPath(p)
}

func (d *Driver) fullPath(p string) (string, error) {
	normalized, err := storage.NormalizePath(p)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(d.root, filepath.FromSlash(storage.JoinPrefix(d.prefix, normalized)))
	rel, err := filepath.Rel(d.root, joined)
	if err != nil {
		return "", fmt.Errorf("storage: compute relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%w: path escapes root", storage.ErrForbidden)
	}
	return joined, nil
}

func (d *Driver) userRelative(target string) (string, error) {
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
		return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
	}
	if errorsIsPermission(err) {
		return fmt.Errorf("%w: %v", storage.ErrForbidden, err)
	}
	return err
}

func errorsIsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err)
}

func errorsIsPermission(err error) bool {
	return errors.Is(err, fs.ErrPermission) || os.IsPermission(err)
}
