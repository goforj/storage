package rclone

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

	// Backends (all)
	_ "github.com/rclone/rclone/backend/all"

	"github.com/goforj/filesystem"
)

func init() {
	filesystem.RegisterDriver("rclone", New)
}

type Driver struct {
	fs     fs.Fs
	prefix string
}

var (
	initOnce       sync.Once
	initErr        error
	initConfigPath string
	initConfigData string
)

// New constructs an rclone-backed filesystem. All disks share a single config path.
// @group Drivers
//
// Example: rclone driver
//
//	fs, _ := rclone.New(context.Background(), filesystem.DiskConfig{Remote: "myremote:bucket"}, filesystem.Config{RcloneConfigData: "[myremote]\ntype = local\n"})
func New(ctx context.Context, cfg filesystem.DiskConfig, global filesystem.Config) (filesystem.Filesystem, error) {
	if cfg.Remote == "" {
		return nil, fmt.Errorf("filesystem: rclone driver requires remote")
	}

	prefix, err := filesystem.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	if err := initRclone(global); err != nil {
		return nil, err
	}

	rcloneFS, err := fs.NewFs(ctx, cfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("filesystem: create rclone fs: %w", err)
	}

	return &Driver{
		fs:     rcloneFS,
		prefix: prefix,
	}, nil
}

func (d *Driver) Get(ctx context.Context, p string) ([]byte, error) {
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

func (d *Driver) Put(ctx context.Context, p string, contents []byte) error {
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

func (d *Driver) Delete(ctx context.Context, p string) error {
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

func (d *Driver) Exists(ctx context.Context, p string) (bool, error) {
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

func (d *Driver) List(ctx context.Context, p string) ([]filesystem.Entry, error) {
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

	var result []filesystem.Entry
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

		result = append(result, filesystem.Entry{
			Path:  rel,
			Size:  size,
			IsDir: isDir,
		})
	}
	return result, nil
}

func (d *Driver) URL(ctx context.Context, p string) (string, error) {
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
		return "", fmt.Errorf("%w: %v", filesystem.ErrUnsupported, err)
	}
	return url, nil
}

// ModTime returns the object's mod time. Intended for testing only.
func (d *Driver) ModTime(ctx context.Context, p string) (time.Time, error) {
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

func initRclone(cfg filesystem.Config) error {
	if cfg.RcloneConfigData != "" {
		if cfg.RcloneConfigPath != "" {
			return fmt.Errorf("filesystem: only one of RcloneConfigPath or RcloneConfigData may be set")
		}
		storage, err := newMemoryStorage(cfg.RcloneConfigData)
		if err != nil {
			return err
		}
		initConfigPath = "inline-rclone.conf"
		initConfigData = cfg.RcloneConfigData
		if err := config.SetConfigPath(initConfigPath); err != nil {
			return err
		}
		config.SetData(storage)
		return nil
	}

	initOnce.Do(func() {
		if cfg.RcloneConfigPath != "" {
			initConfigPath = cfg.RcloneConfigPath
			if err := config.SetConfigPath(initConfigPath); err != nil {
				initErr = err
				return
			}
			configfile.Install()
		}
	})

	if initErr != nil {
		return initErr
	}
	if cfg.RcloneConfigPath != "" && initConfigPath != cfg.RcloneConfigPath {
		return fmt.Errorf("filesystem: rclone already initialized with config path %q", initConfigPath)
	}
	return nil
}

func (d *Driver) fullPath(p string) (string, error) {
	normalized, err := filesystem.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return filesystem.JoinPrefix(d.prefix, normalized), nil
}

func (d *Driver) stripPrefix(remote string) string {
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
		return fmt.Errorf("%w: %v", filesystem.ErrNotFound, err)
	case errors.Is(err, fs.ErrorPermissionDenied):
		return fmt.Errorf("%w: %v", filesystem.ErrForbidden, err)
	case errors.Is(err, hash.ErrUnsupported):
		return fmt.Errorf("%w: %v", filesystem.ErrUnsupported, err)
	}
	return err
}

func isNotFound(err error) bool {
	return errors.Is(err, fs.ErrorObjectNotFound) ||
		errors.Is(err, fs.ErrorDirNotFound) ||
		errors.Is(err, fs.ErrorNotAFile)
}
