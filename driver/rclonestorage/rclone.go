package rclonestorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
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

	"github.com/goforj/storage"
)

func init() {
	storage.RegisterDriver("rclone", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	fs     fs.Fs
	prefix string
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

func (Config) DriverName() string { return "rclone" }

func (c Config) ResolvedConfig() storage.ResolvedConfig {
	return storage.ResolvedConfig{
		Driver:           "rclone",
		Remote:           c.Remote,
		Prefix:           c.Prefix,
		RcloneConfigPath: c.RcloneConfigPath,
		RcloneConfigData: c.RcloneConfigData,
	}
}

var (
	initOnce       sync.Once
	initErr        error
	initConfigPath string
	initConfigData string
	setConfigPath  = config.SetConfigPath
	installConfig  = configfile.Install
)

// New constructs an rclone-backed storage. All disks share a single config path.
// @group Driver Constructors
//
// Example: rclone storage
//
//	fs, _ := rclonestorage.New(context.Background(), rclonestorage.Config{
//		Remote: "local:",
//		Prefix: "sandbox",
//	})
//	_ = fs
func New(ctx context.Context, cfg Config) (storage.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	if cfg.Remote == "" {
		return nil, fmt.Errorf("storage: rclone storage requires remote")
	}

	prefix, err := storage.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	if err := initRclone(cfg); err != nil {
		return nil, err
	}

	rcloneFS, err := fs.NewFs(ctx, cfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("storage: create rclone fs: %w", err)
	}

	return &driver{
		fs:     rcloneFS,
		prefix: prefix,
	}, nil
}

func (d *driver) Get(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	obj, err := d.fs.NewObject(ctx, remote)
	if err != nil {
		return nil, wrapError(err)
	}
	rc, err := obj.Open(ctx)
	if err != nil {
		return nil, wrapError(err)
	}
	defer fs.CheckClose(rc, &err)

	data, readErr := io.ReadAll(rc)
	if readErr != nil {
		return nil, wrapError(readErr)
	}
	return data, nil
}

func (d *driver) Put(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return err
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

func (d *driver) Delete(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return err
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

func (d *driver) Exists(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return false, err
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

func (d *driver) List(ctx context.Context, p string) ([]storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	entries, err := d.fs.List(ctx, remote)
	if err != nil {
		return nil, wrapError(err)
	}

	var result []storage.Entry
	for _, entry := range entries {
		rel := d.stripPrefix(entry.Remote())
		if rel == "" {
			rel = entry.Remote()
		}

		isDir := false
		size := entry.Size()
		if _, ok := entry.(fs.Directory); ok {
			isDir = true
			size = 0
		}

		result = append(result, storage.Entry{
			Path:  rel,
			Size:  size,
			IsDir: isDir,
		})
	}
	return result, nil
}

func (d *driver) Walk(ctx context.Context, p string, fn func(storage.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return err
	}
	return walk.ListR(ctx, d.fs, remote, true, -1, walk.ListAll, func(entries fs.DirEntries) error {
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
			if err := fn(storage.Entry{Path: rel, Size: size, IsDir: isDir}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *driver) URL(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return "", err
	}
	url, err := operations.PublicLink(ctx, d.fs, remote, 0, false)
	if err != nil {
		if isNotFound(err) || errors.Is(err, fs.ErrorPermissionDenied) {
			return "", wrapError(err)
		}
		return "", fmt.Errorf("%w: %v", storage.ErrUnsupported, err)
	}
	return url, nil
}

// ModTime returns the object's mod time. Intended for testing only.
func (d *driver) ModTime(ctx context.Context, p string) (time.Time, error) {
	remote, err := d.fullPath(p)
	if err != nil {
		return time.Time{}, err
	}
	obj, err := d.fs.NewObject(ctx, remote)
	if err != nil {
		return time.Time{}, wrapError(err)
	}
	return obj.ModTime(ctx).UTC(), nil
}

func initRclone(cfg storage.ResolvedConfig) error {
	if cfg.RcloneConfigData != "" {
		if cfg.RcloneConfigPath != "" {
			return fmt.Errorf("storage: only one of RcloneConfigPath or RcloneConfigData may be set")
		}
		storage, err := newMemoryStorage(cfg.RcloneConfigData)
		if err != nil {
			return err
		}
		initConfigPath = "inline-rclone.conf"
		initConfigData = cfg.RcloneConfigData
		if err := setConfigPath(initConfigPath); err != nil {
			return err
		}
		config.SetData(storage)
		return nil
	}

	initOnce.Do(func() {
		if cfg.RcloneConfigPath != "" {
			initConfigPath = cfg.RcloneConfigPath
			if err := setConfigPath(initConfigPath); err != nil {
				initErr = err
				return
			}
			installConfig()
		}
	})

	if initErr != nil {
		return initErr
	}
	if cfg.RcloneConfigPath != "" && initConfigPath != cfg.RcloneConfigPath {
		return fmt.Errorf("storage: rclone already initialized with config path %q", initConfigPath)
	}
	return nil
}

func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storage.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storage.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) stripPrefix(remote string) string {
	if d.prefix == "" {
		return remote
	}
	trimmed := strings.TrimPrefix(remote, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func wrapError(err error) error {
	switch {
	case isNotFound(err):
		return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
	case errors.Is(err, fs.ErrorPermissionDenied):
		return fmt.Errorf("%w: %v", storage.ErrForbidden, err)
	case errors.Is(err, hash.ErrUnsupported):
		return fmt.Errorf("%w: %v", storage.ErrUnsupported, err)
	}
	return err
}

func isNotFound(err error) bool {
	return errors.Is(err, fs.ErrorObjectNotFound) ||
		errors.Is(err, fs.ErrorDirNotFound) ||
		errors.Is(err, fs.ErrorNotAFile)
}
