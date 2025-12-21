package gcsdriver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/goforj/filesystem"
)

func init() {
	filesystem.RegisterDriver("gcs", New)
}

type Driver struct {
	client *storage.Client
	bucket string
	prefix string
}

func New(ctx context.Context, cfg filesystem.DiskConfig, _ filesystem.Config) (filesystem.Filesystem, error) {
	if cfg.GCSBucket == "" {
		return nil, fmt.Errorf("filesystem: gcs driver requires GCSBucket")
	}
	prefix, err := filesystem.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	client, err := newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Driver{
		client: client,
		bucket: cfg.GCSBucket,
		prefix: prefix,
	}, nil
}

func newClient(ctx context.Context, cfg filesystem.DiskConfig) (*storage.Client, error) {
	var opts []option.ClientOption
	if cfg.GCSCredentialsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(cfg.GCSCredentialsJSON)))
	}
	if cfg.GCSEndpoint != "" {
		opts = append(opts, option.WithEndpoint(cfg.GCSEndpoint))
	}
	return storage.NewClient(ctx, opts...)
}

func (d *Driver) Get(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	rc, err := d.client.Bucket(d.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, wrapError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func (d *Driver) Put(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	w := d.client.Bucket(d.bucket).Object(key).NewWriter(ctx)
	w.ChunkSize = 0
	if _, err := io.Copy(w, bytes.NewReader(contents)); err != nil {
		_ = w.Close()
		return wrapError(err)
	}
	if err := w.Close(); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *Driver) Delete(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if err := d.client.Bucket(d.bucket).Object(key).Delete(ctx); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *Driver) Exists(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := d.key(p)
	if err != nil {
		return false, err
	}
	_, err = d.client.Bucket(d.bucket).Object(key).Attrs(ctx)
	if err != nil {
		if err == storage.ErrObjectNotExist {
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
	prefix, err := d.key(p)
	if err != nil {
		return nil, err
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	it := d.client.Bucket(d.bucket).Objects(ctx, &storage.Query{
		Prefix:    prefix,
		Delimiter: "/",
	})

	var entries []filesystem.Entry
	for {
		obj, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, wrapError(err)
		}
		if obj.Prefix != "" {
			rel := strings.TrimSuffix(d.stripPrefix(obj.Prefix), "/")
			if rel != "" {
				entries = append(entries, filesystem.Entry{Path: rel, IsDir: true})
			}
			continue
		}
		rel := d.stripPrefix(obj.Name)
		if rel == "" {
			continue
		}
		entries = append(entries, filesystem.Entry{
			Path:  rel,
			Size:  obj.Size,
			IsDir: false,
		})
	}
	return entries, nil
}

func (d *Driver) URL(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key, err := d.key(p)
	if err != nil {
		return "", err
	}
	url, err := d.client.Bucket(d.bucket).SignedURL(key, &storage.SignedURLOptions{
		Method:  "GET",
		Expires: time.Now().Add(15 * time.Minute),
	})
	if err != nil {
		return "", wrapError(err)
	}
	return url, nil
}

func (d *Driver) key(p string) (string, error) {
	normalized, err := filesystem.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return filesystem.JoinPrefix(d.prefix, normalized), nil
}

func (d *Driver) stripPrefix(k string) string {
	if d.prefix == "" {
		return k
	}
	trimmed := strings.TrimPrefix(k, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func wrapError(err error) error {
	if err == storage.ErrObjectNotExist {
		return fmt.Errorf("%w: %v", filesystem.ErrNotFound, err)
	}
	return err
}
