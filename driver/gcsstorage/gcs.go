package gcsstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/goforj/storage/storagecore"
)

// init registers package integration before callers construct storage.
func init() {
	storagecore.RegisterDriver("gcs", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client    gcsClient
	bucket    string
	prefix    string
	emulator  bool
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

type gcsClient interface {
	Bucket(name string) gcsBucketHandle
	Close() error
}

type gcsBucketHandle interface {
	Object(name string) gcsObjectHandle
	Objects(ctx context.Context, q *storage.Query) gcsObjectIterator
	SignedURL(name string, opts *storage.SignedURLOptions) (string, error)
}

type gcsObjectHandle interface {
	NewReader(ctx context.Context) (io.ReadCloser, error)
	NewWriter(ctx context.Context) gcsWriter
	Delete(ctx context.Context) error
	Attrs(ctx context.Context) (*storage.ObjectAttrs, error)
}

type gcsWriter interface {
	io.WriteCloser
	CloseWithError(err error) error
}

type gcsObjectIterator interface {
	Next() (*storage.ObjectAttrs, error)
}

type realGCSClient struct {
	client *storage.Client
}

type realGCSBucket struct {
	bucket *storage.BucketHandle
}

type realGCSObject struct {
	object *storage.ObjectHandle
}

type realGCSObjectIterator struct {
	iterator *storage.ObjectIterator
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

// DriverName returns the registry identifier for Google Cloud Storage.
func (Config) DriverName() string { return "gcs" }

// ResolvedConfig maps GCS-specific settings into storagecore's shared configuration.
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

// NewContext validates cfg and creates a context-aware GCS store.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig validates the bucket and prefix before constructing the GCS client.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

// newClient configures credentials and emulator transport without inheriting ambient auth for emulators.
func newClient(ctx context.Context, cfg storagecore.ResolvedConfig) (gcsClient, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var opts []option.ClientOption
	if cfg.GCSCredentialsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(cfg.GCSCredentialsJSON)))
	}
	if cfg.GCSEndpoint != "" {
		endpoint, err := normalizeEndpoint(cfg.GCSEndpoint)
		if err != nil {
			return nil, err
		}
		opts = append(opts, option.WithEndpoint(endpoint))
		if cfg.GCSCredentialsJSON == "" {
			opts = append(opts, option.WithoutAuthentication())
		}
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return realGCSClient{client: client}, nil
}

// Get retrieves an object using a background context.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext reads one object and preserves both transfer and reader-close failures.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
	if err != nil {
		return nil, err
	}
	if d.stripPrefix(key) == "" {
		return nil, fmt.Errorf("%w: logical root cannot be read as an object", storagecore.ErrForbidden)
	}
	rc, err := d.client.Bucket(d.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, wrapError(err)
	}
	var data bytes.Buffer
	_, err = copyContext(ctx, &data, rc)
	closeErr := rc.Close()
	if err != nil {
		return nil, joinCleanup(wrapError(err), wrapError(closeErr))
	}
	if closeErr != nil {
		return nil, wrapError(closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

// Put stores an object using a background context.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir creates a directory marker using a background context.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext uploads an object and aborts the writer if copying or cancellation fails.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	w := d.client.Bucket(d.bucket).Object(key).NewWriter(ctx)
	if _, err := copyContext(ctx, w, bytes.NewReader(contents)); err != nil {
		return joinCleanup(wrapError(err), wrapError(w.CloseWithError(err)))
	}
	if err := ctx.Err(); err != nil {
		return joinCleanup(err, wrapError(w.CloseWithError(err)))
	}
	if err := w.Close(); err != nil {
		return wrapError(err)
	}
	return nil
}

// MakeDirContext writes an empty trailing-slash marker and treats the logical root as existing.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" {
		return nil
	}
	w := d.client.Bucket(d.bucket).Object(key + "/").NewWriter(ctx)
	if err := w.Close(); err != nil {
		return wrapError(err)
	}
	return nil
}

// Delete removes one object or empty directory marker using a background context.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext rejects non-empty virtual directories before deleting their marker.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(key) == "" {
		return fmt.Errorf("%w: logical root cannot be deleted", storagecore.ErrForbidden)
	}
	entry, err := d.StatContext(ctx, p)
	if err != nil {
		return err
	}
	if entry.IsDir {
		marker := key + "/"
		it := d.client.Bucket(d.bucket).Objects(ctx, &storage.Query{Prefix: marker})
		for {
			attrs, iterErr := it.Next()
			if iterErr == iterator.Done {
				break
			}
			if iterErr != nil {
				return wrapError(iterErr)
			}
			if attrs.Name != marker {
				return fmt.Errorf("%w: directory not empty", storagecore.ErrForbidden)
			}
		}
		key = marker
	}
	if err := d.client.Bucket(d.bucket).Object(key).Delete(ctx); err != nil {
		return wrapError(err)
	}
	return nil
}

// Stat inspects a logical GCS path using a background context.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext distinguishes objects, explicit markers, and implicit prefix directories.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return storagecore.Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if d.stripPrefix(key) == "" {
		return storagecore.Entry{IsDir: true}, nil
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
	it := d.client.Bucket(d.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	if _, iterErr := it.Next(); iterErr == nil {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	} else if iterErr != iterator.Done {
		return storagecore.Entry{}, wrapError(iterErr)
	}
	return storagecore.Entry{}, wrapError(err)
}

// Exists reports object presence using a background context.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext checks concrete object attributes and does not report virtual directories.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := d.key(p)
	if err != nil {
		return false, err
	}
	if d.stripPrefix(key) == "" {
		return false, nil
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

// List returns immediate GCS children using a background context.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage paginates immediate GCS children using a background context.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext uses GCS delimiter queries to return sorted immediate logical children.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
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

	var entries []storagecore.Entry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

// ListPageContext paginates the deterministic snapshot returned by ListContext.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

// Walk traverses a logical GCS subtree using a background context.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext scans one object prefix, synthesizes parent directories, and emits sorted entries.
func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("%w: walk callback is required", storagecore.ErrForbidden)
	}
	prefix, err := d.key(p)
	if err != nil {
		return err
	}
	requestedPath := d.stripPrefix(prefix)
	if prefix != "" {
		if requestedPath != "" {
			if attrs, err := d.client.Bucket(d.bucket).Object(prefix).Attrs(ctx); err == nil {
				return fn(storagecore.Entry{Path: requestedPath, Size: attrs.Size})
			} else if !isNotFound(err) {
				return wrapError(err)
			}
		}
		prefix += "/"
	}

	seenDirs := map[string]struct{}{}
	entryByPath := map[string]storagecore.Entry{}
	found := requestedPath == ""
	it := d.client.Bucket(d.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
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
		if obj.Name == "" {
			continue
		}
		found = true
		if strings.HasSuffix(obj.Name, "/") {
			dir := strings.TrimSuffix(d.stripPrefix(obj.Name), "/")
			if dir != "" && dir != requestedPath && (requestedPath == "" || strings.HasPrefix(dir, requestedPath+"/")) {
				entryByPath[dir] = storagecore.Entry{Path: dir, IsDir: true}
				for _, parent := range recursiveParentDirs(dir + "/marker") {
					if parent != requestedPath && (requestedPath == "" || strings.HasPrefix(parent, requestedPath+"/")) {
						entryByPath[parent] = storagecore.Entry{Path: parent, IsDir: true}
					}
				}
			}
			continue
		}
		rel := d.stripPrefix(obj.Name)
		if rel == "" {
			continue
		}
		for _, dir := range recursiveParentDirs(rel) {
			if dir == requestedPath || (requestedPath != "" && !strings.HasPrefix(dir, requestedPath+"/")) {
				continue
			}
			if _, ok := seenDirs[dir]; ok {
				continue
			}
			seenDirs[dir] = struct{}{}
			entryByPath[dir] = storagecore.Entry{Path: dir, IsDir: true}
		}
		entryByPath[rel] = storagecore.Entry{
			Path:  rel,
			Size:  obj.Size,
			IsDir: false,
		}
	}
	if !found {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	entries := make([]storagecore.Entry, 0, len(entryByPath))
	for _, entry := range entryByPath {
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return nil
}

// Copy duplicates an object using a background context.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext validates both paths and preserves a normalized same-path object unchanged.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	srcPath, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	dstPath, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("%w: logical root cannot be copied", storagecore.ErrForbidden)
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	if srcPath == dstPath {
		return nil
	}
	return d.PutContext(ctx, dst, data)
}

// Move relocates an object or directory tree using a background context.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext delegates directory trees to storagecore and otherwise copies before deleting.
func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	srcPath, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	dstPath, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("%w: logical root cannot be moved", storagecore.ErrForbidden)
	}
	srcEntry, err := d.StatContext(ctx, src)
	if err != nil {
		return err
	}
	if srcPath == dstPath {
		return nil
	}
	if srcEntry.IsDir {
		return storagecore.MoveDirContext(ctx, d, src, dst)
	}
	if err := d.CopyContext(ctx, src, dst); err != nil {
		return err
	}
	return d.DeleteContext(ctx, src)
}

// URL creates an object URL using a background context.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext creates a fifteen-minute signed GET URL and rejects emulator-backed stores.
func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return "", err
	}
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
	if d.stripPrefix(key) == "" {
		return "", fmt.Errorf("%w: logical root does not have an object URL", storagecore.ErrForbidden)
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

// key normalizes a logical path before joining the configured object prefix.
func (d *driver) key(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// stripPrefix removes only an exact configured component from a GCS object name.
func (d *driver) stripPrefix(k string) string {
	if d.prefix == "" {
		return k
	}
	if k == d.prefix {
		return ""
	}
	return strings.TrimPrefix(k, d.prefix+"/")
}

// recursiveParentDirs returns each directory implied by an object path from shallowest to deepest.
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

// wrapError maps GCS missing and authorization failures to portable storage errors.
func wrapError(err error) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	}
	if isForbidden(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	return err
}

// isForbidden recognizes GCS API responses for unauthenticated or unauthorized requests.
func isForbidden(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && (apiErr.Code == http.StatusForbidden || apiErr.Code == http.StatusUnauthorized)
}

// normalizeEndpoint validates an emulator URL and supplies GCS's JSON API path when omitted.
func normalizeEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("storage: gcs endpoint must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("storage: gcs endpoint must not contain user info, a query, or a fragment")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/storage/v1/"
	}
	return strings.TrimRight(parsed.String(), "/") + "/", nil
}

// joinCleanup preserves a primary transfer failure while retaining cleanup errors.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// copyContext transfers bytes while checking cancellation between read cycles.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

// Close terminally releases the GCS client exactly once.
func (d *driver) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		if d.client != nil {
			d.closeErr = d.client.Close()
		}
	})
	return d.closeErr
}

// closedError prevents work from starting after the GCS client has been released.
func (d *driver) closedError() error {
	if d.closed.Load() {
		return fmt.Errorf("storage: gcs: %w", fs.ErrClosed)
	}
	return nil
}

// isNotFound recognizes both native GCS absence and HTTP 404 responses.
func isNotFound(err error) bool {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return true
	}
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}

// Bucket adapts a concrete GCS bucket handle to the driver's testable interface.
func (c realGCSClient) Bucket(name string) gcsBucketHandle {
	return realGCSBucket{bucket: c.client.Bucket(name)}
}

// Close releases the concrete Google Cloud Storage client.
func (c realGCSClient) Close() error {
	return c.client.Close()
}

// Object adapts one concrete GCS object handle to the driver's testable interface.
func (b realGCSBucket) Object(name string) gcsObjectHandle {
	return realGCSObject{object: b.bucket.Object(name)}
}

// Objects adapts a GCS query iterator to the driver's testable interface.
func (b realGCSBucket) Objects(ctx context.Context, q *storage.Query) gcsObjectIterator {
	return realGCSObjectIterator{iterator: b.bucket.Objects(ctx, q)}
}

// SignedURL delegates signed URL generation to the concrete bucket handle.
func (b realGCSBucket) SignedURL(name string, opts *storage.SignedURLOptions) (string, error) {
	return b.bucket.SignedURL(name, opts)
}

// NewReader opens a context-bound GCS object download.
func (o realGCSObject) NewReader(ctx context.Context) (io.ReadCloser, error) {
	return o.object.NewReader(ctx)
}

// NewWriter disables chunk retries so each testable upload has one completion boundary.
func (o realGCSObject) NewWriter(ctx context.Context) gcsWriter {
	w := o.object.NewWriter(ctx)
	w.ChunkSize = 0
	return w
}

// Delete removes the concrete GCS object represented by this adapter.
func (o realGCSObject) Delete(ctx context.Context) error {
	return o.object.Delete(ctx)
}

// Attrs retrieves metadata for the concrete GCS object represented by this adapter.
func (o realGCSObject) Attrs(ctx context.Context) (*storage.ObjectAttrs, error) {
	return o.object.Attrs(ctx)
}

// Next advances the concrete GCS object iterator.
func (it realGCSObjectIterator) Next() (*storage.ObjectAttrs, error) {
	return it.iterator.Next()
}
