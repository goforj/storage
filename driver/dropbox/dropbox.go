package dropboxdriver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/goforj/filesystem"
)

func init() {
	filesystem.RegisterDriver("dropbox", New)
}

type Driver struct {
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

// New constructs a Dropbox-backed filesystem using the official SDK.
// @group Drivers
//
// Example: dropbox driver
//
//	fs, _ := dropboxdriver.New(context.Background(), filesystem.DiskConfig{Driver: "dropbox", DropboxToken: "token"}, filesystem.Config{})
func New(_ context.Context, cfg filesystem.DiskConfig, _ filesystem.Config) (filesystem.Filesystem, error) {
	if cfg.DropboxToken == "" {
		return nil, fmt.Errorf("filesystem: dropbox driver requires DropboxToken")
	}
	prefix, err := filesystem.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	dbx := files.New(dropbox.Config{
		Token:    cfg.DropboxToken,
		LogLevel: dropbox.LogOff,
	})
	return &Driver{client: dbx, prefix: prefix}, nil
}

func (d *Driver) Get(ctx context.Context, p string) ([]byte, error) {
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

func (d *Driver) Put(ctx context.Context, p string, contents []byte) error {
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

func (d *Driver) Delete(ctx context.Context, p string) error {
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

func (d *Driver) Exists(ctx context.Context, p string) (bool, error) {
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

func (d *Driver) List(ctx context.Context, p string) ([]filesystem.Entry, error) {
	full, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	arg := files.NewListFolderArg(full)
	arg.Recursive = false

	var entries []filesystem.Entry
	err = d.listPage(ctx, arg, &entries)
	if err != nil {
		return nil, wrapError(err)
	}
	return entries, nil
}

func (d *Driver) listPage(ctx context.Context, arg *files.ListFolderArg, entries *[]filesystem.Entry) error {
	res, err := d.client.ListFolder(arg)
	if err != nil {
		return err
	}
	for _, e := range res.Entries {
		switch m := e.(type) {
		case *files.FileMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, filesystem.Entry{
				Path:  rel,
				Size:  int64(m.Size),
				IsDir: false,
			})
		case *files.FolderMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, filesystem.Entry{
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

func (d *Driver) listContinue(ctx context.Context, arg *files.ListFolderContinueArg, entries *[]filesystem.Entry) error {
	res, err := d.client.ListFolderContinue(arg)
	if err != nil {
		return err
	}
	for _, e := range res.Entries {
		switch m := e.(type) {
		case *files.FileMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, filesystem.Entry{
				Path:  rel,
				Size:  int64(m.Size),
				IsDir: false,
			})
		case *files.FolderMetadata:
			rel := d.stripPrefix(m.PathLower)
			*entries = append(*entries, filesystem.Entry{
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

func (d *Driver) URL(ctx context.Context, p string) (string, error) {
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

func (d *Driver) fullPath(p string) (string, error) {
	normalized, err := filesystem.NormalizePath(p)
	if err != nil {
		return "", err
	}
	joined := filesystem.JoinPrefix(d.prefix, normalized)
	if joined == "" {
		return "/", nil
	}
	return "/" + joined, nil
}

func (d *Driver) stripPrefix(p string) string {
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
		return fmt.Errorf("%w: %v", filesystem.ErrNotFound, err)
	}
	return err
}

func isNotFound(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not_found")
}
