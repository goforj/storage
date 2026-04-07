package rclonestorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
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

	"github.com/goforj/storage/storagecore"
)

func init() {
	storagecore.RegisterDriver("rclone", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
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

func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	if cfg.Remote == "" {
		return nil, fmt.Errorf("storage: rclone storage requires remote")
	}

	prefix, err := storagecore.NormalizePath(cfg.Prefix)
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

func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
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

func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
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

func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if remote == "" || remote == "." {
		return nil
	}
	if err := d.fs.Mkdir(ctx, remote); err != nil {
		return wrapError(err)
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

func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	remote, err := d.fullPath(p)
	if err != nil {
		return storagecore.Entry{}, err
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

func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
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
	remote, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	entries, err := d.fs.List(ctx, remote)
	if err != nil {
		return nil, wrapError(err)
	}

	var result []storagecore.Entry
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

func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
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
			if err := fn(storagecore.Entry{Path: rel, Size: size, IsDir: isDir}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	return d.PutContext(ctx, dst, data)
}

func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	srcEntry, err := d.StatContext(ctx, src)
	if err != nil {
		return err
	}
	srcRemote, err := d.fullPath(src)
	if err != nil {
		return err
	}
	dstRemote, err := d.fullPath(dst)
	if err != nil {
		return err
	}
	if srcEntry.IsDir {
		return operations.DirMove(ctx, d.fs, srcRemote, dstRemote)
	}
	if err := d.CopyContext(ctx, src, dst); err != nil {
		return err
	}
	return d.DeleteContext(ctx, src)
}

func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
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
		return "", fmt.Errorf("%w: %v", storagecore.ErrUnsupported, err)
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

func initRclone(cfg storagecore.ResolvedConfig) error {
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
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
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
		return fmt.Errorf("%w: %v", storagecore.ErrNotFound, err)
	case errors.Is(err, fs.ErrorPermissionDenied):
		return fmt.Errorf("%w: %v", storagecore.ErrForbidden, err)
	case errors.Is(err, hash.ErrUnsupported):
		return fmt.Errorf("%w: %v", storagecore.ErrUnsupported, err)
	}
	return err
}

func isNotFound(err error) bool {
	return errors.Is(err, fs.ErrorObjectNotFound) ||
		errors.Is(err, fs.ErrorDirNotFound) ||
		errors.Is(err, fs.ErrorNotAFile)
}
