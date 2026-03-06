package dropboxstorage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/goforj/storage"
)

func init() {
	storage.RegisterDriver("dropbox", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client dropboxClient
	prefix string
}

type dropboxClient interface {
	Download(*files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error)
	Upload(*files.UploadArg, io.Reader) (*files.FileMetadata, error)
	DeleteV2(*files.DeleteArg) (*files.DeleteResult, error)
	GetMetadata(*files.GetMetadataArg) (files.IsMetadata, error)
	ListFolder(*files.ListFolderArg) (*files.ListFolderResult, error)
	ListFolderContinue(*files.ListFolderContinueArg) (*files.ListFolderResult, error)
	GetTemporaryLink(*files.GetTemporaryLinkArg) (*files.GetTemporaryLinkResult, error)
}

// Config defines a Dropbox-backed storage disk.
// @group Drivers
//
// Example: define dropbox storage config
//
//	cfg := dropboxstorage.Config{
//		Token: "token",
//	}
//	_ = cfg
type Config struct {
	Token  string
	Prefix string
}

func (Config) DriverName() string { return "dropbox" }

func (c Config) ResolvedConfig() storage.ResolvedConfig {
	return storage.ResolvedConfig{
		Driver:       "dropbox",
		DropboxToken: c.Token,
		Prefix:       c.Prefix,
	}
}

// New constructs Dropbox-backed storage using the official SDK.
// @group Drivers
//
// Example: dropbox storage
//
//	fs, _ := dropboxstorage.New(context.Background(), dropboxstorage.Config{
//		Token: "token",
//	})
//	_ = fs
func New(ctx context.Context, cfg Config) (storage.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(_ context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	if cfg.DropboxToken == "" {
		return nil, fmt.Errorf("storage: dropbox storage requires DropboxToken")
	}
	prefix, err := storage.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	dbx := files.New(dropbox.Config{
		Token:    cfg.DropboxToken,
		LogLevel: dropbox.LogOff,
	})
	return &driver{client: dbx, prefix: prefix}, nil
}

func (d *driver) Get(ctx context.Context, p string) ([]byte, error) {
	full, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	_, content, err := d.client.Download(files.NewDownloadArg(full))
	if err != nil {
		return nil, wrapError(err)
	}
	defer content.Close()
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func (d *driver) Put(ctx context.Context, p string, contents []byte) error {
	full, err := d.fullPath(p)
	if err != nil {
		return err
	}
	_, err = d.client.Upload(files.NewUploadArg(full), bytes.NewReader(contents))
	if err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *driver) Delete(ctx context.Context, p string) error {
	full, err := d.fullPath(p)
	if err != nil {
		return err
	}
	_, err = d.client.DeleteV2(files.NewDeleteArg(full))
	if err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *driver) Exists(ctx context.Context, p string) (bool, error) {
	full, err := d.fullPath(p)
	if err != nil {
		return false, err
	}
	_, err = d.client.GetMetadata(files.NewGetMetadataArg(full))
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, wrapError(err)
	}
	return true, nil
}

func (d *driver) List(ctx context.Context, p string) ([]storage.Entry, error) {
	full, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	arg := files.NewListFolderArg(full)
	arg.Recursive = false

	var entries []storage.Entry
	err = d.listPage(ctx, arg, &entries)
	if err != nil {
		return nil, wrapError(err)
	}
	return entries, nil
}

func (d *driver) listPage(ctx context.Context, arg *files.ListFolderArg, entries *[]storage.Entry) error {
	res, err := d.client.ListFolder(arg)
	if err != nil {
		return err
	}
	for _, e := range res.Entries {
		switch m := e.(type) {
		case *files.FileMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, storage.Entry{
				Path:  rel,
				Size:  int64(m.Size),
				IsDir: false,
			})
		case *files.FolderMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, storage.Entry{
				Path:  rel,
				Size:  0,
				IsDir: true,
			})
		}
	}
	if res.HasMore {
		continueArg := files.NewListFolderContinueArg(res.Cursor)
		return d.listContinue(ctx, continueArg, entries)
	}
	return nil
}

func (d *driver) listContinue(ctx context.Context, arg *files.ListFolderContinueArg, entries *[]storage.Entry) error {
	res, err := d.client.ListFolderContinue(arg)
	if err != nil {
		return err
	}
	for _, e := range res.Entries {
		switch m := e.(type) {
		case *files.FileMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, storage.Entry{
				Path:  rel,
				Size:  int64(m.Size),
				IsDir: false,
			})
		case *files.FolderMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, storage.Entry{
				Path:  rel,
				Size:  0,
				IsDir: true,
			})
		}
	}
	if res.HasMore {
		return d.listContinue(ctx, files.NewListFolderContinueArg(res.Cursor), entries)
	}
	return nil
}

func (d *driver) URL(ctx context.Context, p string) (string, error) {
	full, err := d.fullPath(p)
	if err != nil {
		return "", err
	}
	link, err := d.client.GetTemporaryLink(&files.GetTemporaryLinkArg{
		Path: full,
	})
	if err != nil {
		return "", wrapError(err)
	}
	return link.Link, nil
}

func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storage.NormalizePath(p)
	if err != nil {
		return "", err
	}
	joined := storage.JoinPrefix(d.prefix, normalized)
	if joined == "" {
		return "/", nil
	}
	return "/" + joined, nil
}

func (d *driver) stripPrefix(p string) string {
	lower := strings.TrimPrefix(strings.ToLower(p), "/")
	if d.prefix == "" {
		return lower
	}
	trimmed := strings.TrimPrefix(lower, strings.ToLower(d.prefix))
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func wrapError(err error) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
	}
	return err
}

func isNotFound(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not_found")
}
