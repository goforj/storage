package gcsstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/goforj/storage/storagecore"
)

// TestGCSConstructors verifies bucket, prefix, and emulator URL validation.
func TestGCSConstructors(t *testing.T) {
	t.Run("new missing bucket", func(t *testing.T) {
		_, err := New(Config{})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("url emulator unsupported", func(t *testing.T) {
		d := &driver{emulator: true}
		_, err := d.URLContext(context.Background(), "file.txt")
		if !errors.Is(err, storagecore.ErrUnsupported) {
			t.Fatalf("URLContext error = %v", err)
		}
	})

	t.Run("new invalid prefix", func(t *testing.T) {
		_, err := New(Config{Bucket: "bucket", Prefix: "../bad"})
		if !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("New invalid prefix error = %v", err)
		}
	})
}

// TestGCSKeyAndPrefixHelpers verifies prefix joining and exact-component stripping.
func TestGCSKeyAndPrefixHelpers(t *testing.T) {
	d := &driver{prefix: "pre"}
	k, err := d.key("file.txt")
	if err != nil {
		t.Fatalf("key err: %v", err)
	}
	if k != "pre/file.txt" {
		t.Fatalf("key got %q", k)
	}
	if got := d.stripPrefix("pre/dir/file"); got != "dir/file" {
		t.Fatalf("stripPrefix got %q", got)
	}
}

// TestGCSWrapError verifies native and HTTP absence retain ErrNotFound identity.
func TestGCSWrapError(t *testing.T) {
	if err := wrapError(storage.ErrObjectNotExist); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if !isNotFound(&googleapi.Error{Code: 404}) {
		t.Fatal("isNotFound should detect googleapi 404")
	}
	if err := wrapError(errors.New("other")); errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("wrapError should preserve non-not-found errors")
	}
	if isNotFound(errors.New("other")) {
		t.Fatal("isNotFound should ignore other errors")
	}
}

// TestGCSContextCancellation verifies canceled calls stop before accessing a bucket.
func TestGCSContextCancellation(t *testing.T) {
	d := &driver{prefix: "pre", emulator: false}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := d.GetContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetContext error = %v", err)
	}
	if err := d.PutContext(ctx, "file.txt", []byte("hello")); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext error = %v", err)
	}
	if err := d.DeleteContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext error = %v", err)
	}
	if _, err := d.ExistsContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExistsContext error = %v", err)
	}
	if _, err := d.ListContext(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext error = %v", err)
	}
	if err := d.WalkContext(ctx, "", func(storagecore.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext error = %v", err)
	}
	if err := d.CopyContext(ctx, "file.txt", "copy.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext error = %v", err)
	}
	if err := d.MoveContext(ctx, "file.txt", "moved.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext error = %v", err)
	}
	if _, err := d.URLContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
}

// TestGCSHelpers verifies resolved settings, path validation, and implied parent generation.
func TestGCSHelpers(t *testing.T) {
	cfg := Config{Bucket: "bucket", CredentialsJSON: "{}", Endpoint: "http://127.0.0.1:4443", Prefix: "pre"}
	resolved := cfg.ResolvedConfig()
	if cfg.DriverName() != "gcs" || resolved.GCSBucket != "bucket" || resolved.Prefix != "pre" || resolved.GCSEndpoint == "" {
		t.Fatalf("ResolvedConfig = %+v", resolved)
	}

	d := &driver{}
	if got := d.stripPrefix("plain/path"); got != "plain/path" {
		t.Fatalf("stripPrefix without prefix = %q", got)
	}
	if _, err := d.key("../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("key invalid error = %v", err)
	}
	if got := recursiveParentDirs("a/b/c/file.txt"); len(got) != 3 || got[0] != "a" || got[2] != "a/b/c" {
		t.Fatalf("recursiveParentDirs = %v", got)
	}
	if dirs := recursiveParentDirs("file.txt"); dirs != nil {
		t.Fatalf("recursiveParentDirs file = %v", dirs)
	}
}

type fakeGCSClient struct {
	bucket *fakeGCSBucket
}

// Bucket returns the fake bucket shared by driver tests.
func (f fakeGCSClient) Bucket(string) gcsBucketHandle { return f.bucket }

// Close makes fake client cleanup succeed.
func (f fakeGCSClient) Close() error { return nil }

type fakeGCSBucket struct {
	object      *fakeGCSObject
	objects     []*storage.ObjectAttrs
	objectsErr  error
	signedURL   string
	signedErr   error
	queryPrefix string
}

// Object returns the fake object shared by bucket operations.
func (f *fakeGCSBucket) Object(string) gcsObjectHandle { return f.object }

// Objects filters fixture metadata by query prefix and records that prefix.
func (f *fakeGCSBucket) Objects(_ context.Context, q *storage.Query) gcsObjectIterator {
	items := f.objects
	if q != nil {
		f.queryPrefix = q.Prefix
		items = nil
		for _, item := range f.objects {
			name := item.Name
			if name == "" {
				name = item.Prefix
			}
			if strings.HasPrefix(name, q.Prefix) {
				items = append(items, item)
			}
		}
	}
	return &fakeGCSIterator{items: items, err: f.objectsErr}
}

// SignedURL returns the configured link or signing failure.
func (f *fakeGCSBucket) SignedURL(string, *storage.SignedURLOptions) (string, error) {
	return f.signedURL, f.signedErr
}

type fakeGCSObject struct {
	readData   string
	readErr    error
	readBody   io.ReadCloser
	writeErr   error
	abortErr   error
	abortWith  error
	closeCalls int
	abortCalls int
	writeBuf   bytes.Buffer
	deleteErr  error
	attrs      *storage.ObjectAttrs
	attrsErr   error
}

// NewReader opens the configured body, payload, or read failure.
func (f *fakeGCSObject) NewReader(context.Context) (io.ReadCloser, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.readBody != nil {
		return f.readBody, nil
	}
	return io.NopCloser(bytes.NewBufferString(f.readData)), nil
}

// NewWriter returns a writer that records commit and abort behavior on its object.
func (f *fakeGCSObject) NewWriter(context.Context) gcsWriter {
	return &fakeGCSWriter{parent: f}
}

// Delete returns the configured object-removal failure.
func (f *fakeGCSObject) Delete(context.Context) error {
	return f.deleteErr
}

// Attrs returns configured object metadata or lookup failure.
func (f *fakeGCSObject) Attrs(context.Context) (*storage.ObjectAttrs, error) {
	if f.attrsErr != nil {
		return nil, f.attrsErr
	}
	return f.attrs, nil
}

type fakeGCSWriter struct {
	parent *fakeGCSObject
}

// Write captures uploaded bytes unless a configured transfer failure occurs.
func (w *fakeGCSWriter) Write(p []byte) (int, error) {
	if w.parent.writeErr != nil {
		return 0, w.parent.writeErr
	}
	return w.parent.writeBuf.Write(p)
}

// Close records a committed writer close and returns its configured failure.
func (w *fakeGCSWriter) Close() error {
	w.parent.closeCalls++
	return w.parent.writeErr
}

// CloseWithError records the abort cause and returns any configured cleanup failure.
func (w *fakeGCSWriter) CloseWithError(err error) error {
	w.parent.abortCalls++
	w.parent.abortWith = err
	return w.parent.abortErr
}

type fakeGCSIterator struct {
	items []*storage.ObjectAttrs
	err   error
	idx   int
}

// Next yields configured metadata in order before returning iterator.Done.
func (it *fakeGCSIterator) Next() (*storage.ObjectAttrs, error) {
	if it.err != nil {
		return nil, it.err
	}
	if it.idx >= len(it.items) {
		return nil, iterator.Done
	}
	item := it.items[it.idx]
	it.idx++
	return item, nil
}

// TestGCSFakeBackedOperations verifies core storage operations through the GCS adapter boundary.
func TestGCSFakeBackedOperations(t *testing.T) {
	object := &fakeGCSObject{
		readData: "hello",
		attrs:    &storage.ObjectAttrs{Name: "pre/file.txt", Size: 5},
	}
	bucket := &fakeGCSBucket{
		object: object,
		objects: []*storage.ObjectAttrs{
			{Prefix: "pre/dir/"},
			{Name: "pre/dir/file.txt", Size: 5},
		},
		signedURL: "https://signed.example/file",
	}
	d := &driver{
		client: fakeGCSClient{bucket: bucket},
		bucket: "bucket",
		prefix: "pre",
	}

	data, err := d.Get("file.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("Get = %q err=%v", data, err)
	}
	if err := d.Put("file.txt", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := object.writeBuf.String(); got != "payload" {
		t.Fatalf("written payload = %q", got)
	}
	if err := d.Delete("file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entry, err := d.Stat("file.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry.Path != "file.txt" || entry.Size != 5 {
		t.Fatalf("Stat entry = %+v", entry)
	}
	exists, err := d.Exists("file.txt")
	if err != nil || !exists {
		t.Fatalf("Exists = %v err=%v", exists, err)
	}
	entries, err := d.List("dir")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List entries=%v err=%v", entries, err)
	}
	if bucket.queryPrefix != "pre/dir/" {
		t.Fatalf("List query prefix = %q", bucket.queryPrefix)
	}
	url, err := d.URL("file.txt")
	if err != nil || url == "" {
		t.Fatalf("URL = %q err=%v", url, err)
	}
}

// TestGCSFakeWalkAndErrorBranches verifies traversal, root guards, same-path copy, and cleanup failures.
func TestGCSFakeWalkAndErrorBranches(t *testing.T) {
	t.Run("walk file path", func(t *testing.T) {
		object := &fakeGCSObject{attrs: &storage.ObjectAttrs{Name: "pre/file.txt", Size: 4}}
		bucket := &fakeGCSBucket{
			object:  object,
			objects: []*storage.ObjectAttrs{},
		}
		d := &driver{client: fakeGCSClient{bucket: bucket}, bucket: "bucket", prefix: "pre"}

		var got []storagecore.Entry
		if err := d.Walk("file.txt", func(entry storagecore.Entry) error {
			got = append(got, entry)
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		if len(got) != 1 || got[0].Path != "file.txt" {
			t.Fatalf("Walk entries = %+v", got)
		}
	})

	t.Run("walk recursive and callback error", func(t *testing.T) {
		object := &fakeGCSObject{attrsErr: storage.ErrObjectNotExist}
		bucket := &fakeGCSBucket{
			object: object,
			objects: []*storage.ObjectAttrs{
				{Name: "pre/folder/file-a.txt", Size: 1},
				{Name: "pre/folder/file-b.txt", Size: 2},
			},
		}
		d := &driver{client: fakeGCSClient{bucket: bucket}, bucket: "bucket", prefix: "pre"}

		stop := errors.New("stop")
		err := d.Walk("folder", func(entry storagecore.Entry) error {
			if entry.Path == "folder/file-b.txt" {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) {
			t.Fatalf("Walk callback error = %v", err)
		}
	})

	t.Run("walk omits requested ancestors", func(t *testing.T) {
		object := &fakeGCSObject{attrsErr: storage.ErrObjectNotExist}
		bucket := &fakeGCSBucket{object: object, objects: []*storage.ObjectAttrs{
			{Name: "pre/move-dir/source/file.txt", Size: 1},
		}}
		d := &driver{client: fakeGCSClient{bucket: bucket}, bucket: "bucket", prefix: "pre"}
		var paths []string
		if err := d.Walk("move-dir/source", func(entry storagecore.Entry) error {
			paths = append(paths, entry.Path)
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		if slices.Contains(paths, "move-dir") || slices.Contains(paths, "move-dir/source") {
			t.Fatalf("Walk returned its root or an ancestor: %v", paths)
		}
	})

	t.Run("get put delete stat exists list url errors", func(t *testing.T) {
		object := &fakeGCSObject{
			readErr:   storage.ErrObjectNotExist,
			writeErr:  errors.New("write boom"),
			deleteErr: errors.New("delete boom"),
			attrsErr:  errors.New("attrs boom"),
		}
		bucket := &fakeGCSBucket{
			object:     object,
			objectsErr: errors.New("list boom"),
			signedErr:  errors.New("sign boom"),
		}
		d := &driver{client: fakeGCSClient{bucket: bucket}, bucket: "bucket", prefix: "pre"}

		if _, err := d.Get("file.txt"); !errors.Is(err, storagecore.ErrNotFound) {
			t.Fatalf("Get error = %v", err)
		}
		if err := d.Put("file.txt", []byte("x")); err == nil {
			t.Fatal("Put returned nil error")
		}
		if err := d.Delete("file.txt"); err == nil {
			t.Fatal("Delete returned nil error")
		}
		if _, err := d.Stat("file.txt"); err == nil {
			t.Fatal("Stat returned nil error")
		}
		if _, err := d.Exists("file.txt"); err == nil {
			t.Fatal("Exists returned nil error")
		}
		if _, err := d.List(""); err == nil {
			t.Fatal("List returned nil error")
		}
		if _, err := d.URL("file.txt"); err == nil {
			t.Fatal("URL returned nil error")
		}
	})

	t.Run("read failure after open", func(t *testing.T) {
		object := &fakeGCSObject{readBody: io.NopCloser(errReader{})}
		d := &driver{
			client: fakeGCSClient{bucket: &fakeGCSBucket{object: object}},
			bucket: "bucket",
			prefix: "pre",
		}
		if _, err := d.Get("file.txt"); err == nil {
			t.Fatal("Get returned nil error")
		}
	})

	t.Run("put aborts writer without committing after write failure", func(t *testing.T) {
		writeErr := errors.New("write boom")
		abortErr := errors.New("abort boom")
		object := &fakeGCSObject{writeErr: writeErr, abortErr: abortErr}
		d := &driver{
			client: fakeGCSClient{bucket: &fakeGCSBucket{object: object}},
			bucket: "bucket",
			prefix: "pre",
		}
		err := d.Put("file.txt", []byte("payload"))
		if !errors.Is(err, writeErr) || !errors.Is(err, abortErr) {
			t.Fatalf("Put error = %v", err)
		}
		if object.abortCalls != 1 || object.closeCalls != 0 || !errors.Is(object.abortWith, writeErr) {
			t.Fatalf("writer abortCalls=%d closeCalls=%d abortWith=%v", object.abortCalls, object.closeCalls, object.abortWith)
		}
	})

	t.Run("logical root mutations are rejected", func(t *testing.T) {
		object := &fakeGCSObject{attrs: &storage.ObjectAttrs{Name: "pre"}}
		bucket := &fakeGCSBucket{object: object}
		d := &driver{
			client: fakeGCSClient{bucket: bucket},
			bucket: "bucket",
			prefix: "pre",
		}
		if err := d.MakeDir(""); err != nil {
			t.Fatalf("MakeDir root: %v", err)
		}
		entry, err := d.Stat("")
		if err != nil || !entry.IsDir || entry.Path != "" {
			t.Fatalf("Stat root = %+v err=%v", entry, err)
		}
		if err := d.Walk("", func(storagecore.Entry) error { return nil }); err != nil {
			t.Fatalf("Walk empty root: %v", err)
		}
		if err := d.Put("", []byte("payload")); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Put root error = %v", err)
		}
		if err := d.Delete(""); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Delete root error = %v", err)
		}
		if _, err := d.Get(""); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Get root error = %v", err)
		}
		if _, err := d.URL(""); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("URL root error = %v", err)
		}
		if err := d.Copy("", "copy.txt"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Copy source root error = %v", err)
		}
		if err := d.Copy("file.txt", ""); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Copy target root error = %v", err)
		}
		if object.closeCalls != 0 {
			t.Fatalf("logical root Put committed %d writers", object.closeCalls)
		}
		if err := d.Move("file.txt", "file.txt"); err != nil {
			t.Fatalf("Move same path: %v", err)
		}
	})

	t.Run("walk root confines configured prefix", func(t *testing.T) {
		object := &fakeGCSObject{attrsErr: storage.ErrObjectNotExist}
		bucket := &fakeGCSBucket{
			object: object,
			objects: []*storage.ObjectAttrs{
				{Name: "assets/file.txt", Size: 1},
				{Name: "assets2/leak.txt", Size: 1},
			},
		}
		d := &driver{client: fakeGCSClient{bucket: bucket}, bucket: "bucket", prefix: "assets"}
		var paths []string
		if err := d.Walk("", func(entry storagecore.Entry) error {
			paths = append(paths, entry.Path)
			return nil
		}); err != nil {
			t.Fatalf("Walk root: %v", err)
		}
		if bucket.queryPrefix != "assets/" {
			t.Fatalf("Walk query prefix = %q", bucket.queryPrefix)
		}
		if len(paths) != 1 || paths[0] != "file.txt" {
			t.Fatalf("Walk paths = %v", paths)
		}
	})

	t.Run("same-path copy validates without writing", func(t *testing.T) {
		object := &fakeGCSObject{readData: "payload"}
		d := &driver{client: fakeGCSClient{bucket: &fakeGCSBucket{object: object}}, bucket: "bucket"}
		if err := d.Copy("file.txt", "file.txt"); err != nil {
			t.Fatalf("Copy same path: %v", err)
		}
		if object.closeCalls != 0 {
			t.Fatalf("same-path Copy committed %d writers", object.closeCalls)
		}
		object.readErr = storage.ErrObjectNotExist
		if err := d.Copy("missing.txt", "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
			t.Fatalf("Copy missing same path error = %v", err)
		}
	})
}

type errReader struct{}

// Read injects a deterministic transfer failure after a reader opens successfully.
func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }
