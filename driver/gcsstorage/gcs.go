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
	"path"
	"strings"
	"time"

	gcsapi "cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/goforj/storage/storagecore"
)

func init() {
	storagecore.RegisterDriver("gcs", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client   gcsClient
	bucket   string
	prefix   string
	emulator bool
}

type gcsClient interface {
	Bucket(name string) gcsBucketHandle
}

type gcsBucketHandle interface {
	Object(name string) gcsObjectHandle
	Objects(ctx context.Context, q *gcsapi.Query) gcsObjectIterator
	SignedURL(name string, opts *gcsapi.SignedURLOptions) (string, error)
}

type gcsObjectHandle interface {
	NewReader(ctx context.Context) (io.ReadCloser, error)
	NewWriter(ctx context.Context) gcsWriter
	Delete(ctx context.Context) error
	Attrs(ctx context.Context) (*gcsapi.ObjectAttrs, error)
}

type gcsWriter interface {
	io.WriteCloser
}

type gcsObjectIterator interface {
	Next() (*gcsapi.ObjectAttrs, error)
}

type realGCSClient struct {
	client *gcsapi.Client
}

type realGCSBucket struct {
	bucket *gcsapi.BucketHandle
}

type realGCSObject struct {
	object *gcsapi.ObjectHandle
}

type realGCSObjectIterator struct {
	iterator *gcsapi.ObjectIterator
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

func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
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
//	fs, _ := gcsstorage.New(gcsstorage.Config{
//		Bucket: "uploads",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	if cfg.GCSBucket == "" {
		return nil, fmt.Errorf("storage: gcs storage requires GCSBucket")
	}
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
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

func newClient(ctx context.Context, cfg storagecore.ResolvedConfig) (gcsClient, error) {
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
	client, err := gcsapi.NewClient(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return realGCSClient{client: client}, nil
}

func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
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
	key, err := d.key(p)
	if err != nil {
		return err
	}
	w := d.client.Bucket(d.bucket).Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, bytes.NewReader(contents)); err != nil {
		_ = w.Close()
		return wrapError(err)
	}
	if err := w.Close(); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if key == "" {
		return nil
	}
	w := d.client.Bucket(d.bucket).Object(key + "/").NewWriter(ctx)
	if err := w.Close(); err != nil {
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
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if err := d.client.Bucket(d.bucket).Object(key).Delete(ctx); err != nil {
		if !isNotFound(err) {
			return wrapError(err)
		}
		if err := d.client.Bucket(d.bucket).Object(key + "/").Delete(ctx); err != nil {
			return wrapError(err)
		}
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
	key, err := d.key(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	attrs, err := d.client.Bucket(d.bucket).Object(key).Attrs(ctx)
	if err == nil {
		return storagecore.Entry{Path: d.stripPrefix(key), Size: attrs.Size, IsDir: false}, nil
	}
	if !isNotFound(err) {
		return storagecore.Entry{}, wrapError(err)
	}
	if _, dirErr := d.client.Bucket(d.bucket).Object(key + "/").Attrs(ctx); dirErr == nil {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	} else if !isNotFound(dirErr) {
		return storagecore.Entry{}, wrapError(dirErr)
	}
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	it := d.client.Bucket(d.bucket).Objects(ctx, &gcsapi.Query{Prefix: prefix})
	if _, iterErr := it.Next(); iterErr == nil {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	} else if iterErr != iterator.Done {
		return storagecore.Entry{}, wrapError(iterErr)
	}
	return storagecore.Entry{}, wrapError(err)
}

func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
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

	var entries []storagecore.Entry
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
				entries = append(entries, storagecore.Entry{Path: rel, IsDir: true})
			}
			continue
		}
		if strings.HasSuffix(obj.Name, "/") {
			continue
		}
		rel := d.stripPrefix(obj.Name)
		if rel == "" {
			continue
		}
		entries = append(entries, storagecore.Entry{
			Path:  rel,
			Size:  obj.Size,
			IsDir: false,
		})
	}
	return entries, nil
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
	prefix, err := d.key(p)
	if err != nil {
		return err
	}
	fileExists := false
	if prefix != "" {
		if _, err := d.client.Bucket(d.bucket).Object(prefix).Attrs(ctx); err == nil {
			fileExists = true
		} else if !isNotFound(err) {
			return wrapError(err)
		}
		prefix += "/"
	}

	seenDirs := map[string]struct{}{}
	it := d.client.Bucket(d.bucket).Objects(ctx, &gcsapi.Query{Prefix: prefix})
	for {
		obj, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return wrapError(err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if obj.Name == "" || strings.HasSuffix(obj.Name, "/") {
			continue
		}
		rel := d.stripPrefix(obj.Name)
		if rel == "" {
			continue
		}
		for _, dir := range recursiveParentDirs(rel) {
			if _, ok := seenDirs[dir]; ok {
				continue
			}
			seenDirs[dir] = struct{}{}
			if err := fn(storagecore.Entry{Path: dir, IsDir: true}); err != nil {
				return err
			}
		}
		if err := fn(storagecore.Entry{
			Path:  rel,
			Size:  obj.Size,
			IsDir: false,
		}); err != nil {
			return err
		}
	}
	if fileExists {
		return fn(storagecore.Entry{Path: d.stripPrefix(strings.TrimSuffix(prefix, "/")), IsDir: false})
	}
	return nil
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
	if srcEntry.IsDir {
		return storagecore.MoveDirContext(ctx, d, src, dst)
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
	if d.emulator {
		return "", storagecore.ErrUnsupported
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
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) stripPrefix(k string) string {
	if d.prefix == "" {
		return k
	}
	trimmed := strings.TrimPrefix(k, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func recursiveParentDirs(p string) []string {
	dir := path.Dir(p)
	if dir == "." || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

func wrapError(err error) error {
	if isNotFound(err) {
		return fmt.Errorf("%w: %v", storagecore.ErrNotFound, err)
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

func (c realGCSClient) Bucket(name string) gcsBucketHandle {
	return realGCSBucket{bucket: c.client.Bucket(name)}
}

func (b realGCSBucket) Object(name string) gcsObjectHandle {
	return realGCSObject{object: b.bucket.Object(name)}
}

func (b realGCSBucket) Objects(ctx context.Context, q *gcsapi.Query) gcsObjectIterator {
	return realGCSObjectIterator{iterator: b.bucket.Objects(ctx, q)}
}

func (b realGCSBucket) SignedURL(name string, opts *gcsapi.SignedURLOptions) (string, error) {
	return b.bucket.SignedURL(name, opts)
}

func (o realGCSObject) NewReader(ctx context.Context) (io.ReadCloser, error) {
	return o.object.NewReader(ctx)
}

func (o realGCSObject) NewWriter(ctx context.Context) gcsWriter {
	w := o.object.NewWriter(ctx)
	w.ChunkSize = 0
	return w
}

func (o realGCSObject) Delete(ctx context.Context) error {
	return o.object.Delete(ctx)
}

func (o realGCSObject) Attrs(ctx context.Context) (*gcsapi.ObjectAttrs, error) {
	return o.object.Attrs(ctx)
}

func (it realGCSObjectIterator) Next() (*gcsapi.ObjectAttrs, error) {
	return it.iterator.Next()
}
