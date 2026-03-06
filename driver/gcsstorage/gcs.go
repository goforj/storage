package gcsstorage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	gcsapi "cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/goforj/storage"
)

func init() {
	storage.RegisterDriver("gcs", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client   *gcsapi.Client
	bucket   string
	prefix   string
	emulator bool
}

// Config defines a GCS-backed storage disk.
// @group Driver Config
//
// Example: define gcs storage config
//
//	cfg := gcsstorage.Config{
//		Bucket: "uploads",
//	}
//	_ = cfg
//
// Example: define gcs storage config with all fields
//
//	cfg := gcsstorage.Config{
//		Bucket:          "uploads",
//		CredentialsJSON: "{...}",              // default: ""
//		Endpoint:        "http://127.0.0.1:0", // default: ""
//		Prefix:          "assets",             // default: ""
//	}
//	_ = cfg
type Config struct {
	Bucket          string
	CredentialsJSON string
	Endpoint        string
	Prefix          string
}

func (Config) DriverName() string { return "gcs" }

func (c Config) ResolvedConfig() storage.ResolvedConfig {
	return storage.ResolvedConfig{
		Driver:             "gcs",
		GCSBucket:          c.Bucket,
		GCSCredentialsJSON: c.CredentialsJSON,
		GCSEndpoint:        c.Endpoint,
		Prefix:             c.Prefix,
	}
}

// New constructs GCS-backed storage using cloud.google.com/go/storage.
// @group Driver Constructors
//
// Example: gcs storage
//
//	fs, _ := gcsstorage.New(context.Background(), gcsstorage.Config{
//		Bucket: "uploads",
//	})
//	_ = fs
func New(ctx context.Context, cfg Config) (storage.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	if cfg.GCSBucket == "" {
		return nil, fmt.Errorf("storage: gcs storage requires GCSBucket")
	}
	prefix, err := storage.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	client, err := newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &driver{
		client:   client,
		bucket:   cfg.GCSBucket,
		prefix:   prefix,
		emulator: cfg.GCSEndpoint != "",
	}, nil
}

func newClient(ctx context.Context, cfg storage.ResolvedConfig) (*gcsapi.Client, error) {
	var opts []option.ClientOption
	if cfg.GCSCredentialsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(cfg.GCSCredentialsJSON)))
	}
	if cfg.GCSEndpoint != "" {
		if cfg.GCSCredentialsJSON == "" {
			opts = append(opts, option.WithoutAuthentication())
		}
		if strings.HasPrefix(cfg.GCSEndpoint, "https://") {
			opts = append(opts, option.WithHTTPClient(&http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				},
			}))
		}
		_ = os.Setenv("STORAGE_EMULATOR_HOST", cfg.GCSEndpoint)
	}
	return gcsapi.NewClient(ctx, opts...)
}

func (d *driver) Get(ctx context.Context, p string) ([]byte, error) {
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

func (d *driver) Put(ctx context.Context, p string, contents []byte) error {
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

func (d *driver) Delete(ctx context.Context, p string) error {
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

func (d *driver) Exists(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := d.key(p)
	if err != nil {
		return false, err
	}
	_, err = d.client.Bucket(d.bucket).Object(key).Attrs(ctx)
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
	prefix, err := d.key(p)
	if err != nil {
		return nil, err
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	it := d.client.Bucket(d.bucket).Objects(ctx, &gcsapi.Query{
		Prefix:    prefix,
		Delimiter: "/",
	})

	var entries []storage.Entry
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
				entries = append(entries, storage.Entry{Path: rel, IsDir: true})
			}
			continue
		}
		rel := d.stripPrefix(obj.Name)
		if rel == "" {
			continue
		}
		entries = append(entries, storage.Entry{
			Path:  rel,
			Size:  obj.Size,
			IsDir: false,
		})
	}
	return entries, nil
}

func (d *driver) URL(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if d.emulator {
		return "", storage.ErrUnsupported
	}
	key, err := d.key(p)
	if err != nil {
		return "", err
	}
	url, err := d.client.Bucket(d.bucket).SignedURL(key, &gcsapi.SignedURLOptions{
		Method:  "GET",
		Expires: time.Now().Add(15 * time.Minute),
	})
	if err != nil {
		return "", wrapError(err)
	}
	return url, nil
}

func (d *driver) key(p string) (string, error) {
	normalized, err := storage.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storage.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) stripPrefix(k string) string {
	if d.prefix == "" {
		return k
	}
	trimmed := strings.TrimPrefix(k, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func wrapError(err error) error {
	if isNotFound(err) {
		return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
	}
	return err
}

func isNotFound(err error) bool {
	if errors.Is(err, gcsapi.ErrObjectNotExist) {
		return true
	}
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}
