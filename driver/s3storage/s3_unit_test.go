package s3storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/goforj/storage/storagecore"
)

type fakeS3 struct {
	getErr       error
	putErr       error
	delErr       error
	headErr      error
	listErr      error
	listOut      *s3.ListObjectsV2Output
	listSeq      []*s3.ListObjectsV2Output
	headOK       bool
	getBody      string
	getReader    io.ReadCloser
	getInputs    []*s3.GetObjectInput
	putInputs    []*s3.PutObjectInput
	deleteInputs []*s3.DeleteObjectInput
	headInputs   []*s3.HeadObjectInput
	listInputs   []*s3.ListObjectsV2Input
	headByKey    map[string]*s3.HeadObjectOutput
	headErrByKey map[string]error
}

// GetObject records read ownership and returns the configured body or failure for cleanup assertions.
func (f *fakeS3) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getInputs = append(f.getInputs, in)
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getReader != nil {
		return &s3.GetObjectOutput{Body: f.getReader}, nil
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(f.getBody))}, nil
}

// PutObject records write inputs before returning the configured service failure.
func (f *fakeS3) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putInputs = append(f.putInputs, in)
	return &s3.PutObjectOutput{}, f.putErr
}

// DeleteObject records conditional deletion inputs before returning the configured service failure.
func (f *fakeS3) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteInputs = append(f.deleteInputs, in)
	return &s3.DeleteObjectOutput{}, f.delErr
}

// HeadObject allows per-key metadata and failures so object and directory probes can diverge deterministically.
func (f *fakeS3) HeadObject(ctx context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.headInputs = append(f.headInputs, in)
	key := aws.ToString(in.Key)
	if out, ok := f.headByKey[key]; ok {
		return out, nil
	}
	if err, ok := f.headErrByKey[key]; ok {
		return nil, err
	}
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.headOK {
		return &s3.HeadObjectOutput{}, nil
	}
	return nil, &types.NotFound{}
}

// ListObjectsV2 consumes a configured page sequence so pagination and cancellation boundaries stay deterministic.
func (f *fakeS3) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listInputs = append(f.listInputs, in)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listSeq) > 0 {
		out := f.listSeq[0]
		f.listSeq = f.listSeq[1:]
		return out, nil
	}
	if f.listOut != nil {
		return f.listOut, nil
	}
	return &s3.ListObjectsV2Output{}, nil
}

type fakePresign struct {
	err error
	url string
}

// trackedReadCloser records cleanup and can inject a close failure.
type trackedReadCloser struct {
	reader   io.Reader
	closeErr error
	closed   bool
}

// failingReader returns a stable stream failure for lifecycle tests.
type failingReader struct {
	err error
}

// Read returns the configured stream failure.
func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

// Read delegates stream reads to the configured reader.
func (r *trackedReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

// Close records cleanup before returning the configured failure.
func (r *trackedReadCloser) Close() error {
	r.closed = true
	return r.closeErr
}

// PresignGetObject isolates URL behavior from AWS signing while preserving configured failures.
func (f fakePresign) PresignGetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &v4.PresignedHTTPRequest{URL: f.url}, nil
}

// TestS3StorageOperations covers the basic object lifecycle against the in-memory client.
func TestS3StorageOperations(t *testing.T) {
	client := &fakeS3{headOK: true, getBody: "data"}
	d := &driver{
		client:  client,
		presign: fakePresign{url: "http://signed"},
		bucket:  "b",
		prefix:  "pre",
	}

	if _, err := d.Get("file.txt"); err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if err := d.Put("file.txt", []byte("x")); err != nil {
		t.Fatalf("Put err: %v", err)
	}
	exists, err := d.Exists("file.txt")
	if err != nil || !exists {
		t.Fatalf("Exists err: %v exists %v", err, exists)
	}
	if _, err := d.URL("file.txt"); err != nil {
		t.Fatalf("URL err: %v", err)
	}

	client.listOut = &s3.ListObjectsV2Output{
		CommonPrefixes: []types.CommonPrefix{{Prefix: aws.String("pre/dir/")}},
		Contents:       []types.Object{{Key: aws.String("pre/dir/file.txt"), Size: aws.Int64(5)}},
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List err %v entries %v", err, entries)
	}
	if entries[0].Path != "dir" || entries[1].Path != "dir/file.txt" {
		t.Fatalf("List order = %+v", entries)
	}
}

// TestS3Constructors validates required bucket settings and client-construction failures.
func TestS3Constructors(t *testing.T) {
	if got := (Config{}).DriverName(); got != "s3" {
		t.Fatalf("DriverName = %q", got)
	}

	t.Run("missing bucket", func(t *testing.T) {
		_, err := New(Config{Region: "us-east-1"})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("missing region", func(t *testing.T) {
		_, err := New(Config{Bucket: "bucket"})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("resolved config", func(t *testing.T) {
		cfg := Config{
			Bucket:          "bucket",
			Endpoint:        "http://localhost:9000",
			Region:          "us-east-1",
			AccessKeyID:     "access",
			SecretAccessKey: "secret",
			UsePathStyle:    true,
			UnsignedPayload: true,
			Prefix:          "pre",
		}
		resolved := cfg.ResolvedConfig()
		if resolved.Driver != "s3" || resolved.S3Bucket != "bucket" || !resolved.S3UsePathStyle || !resolved.S3UnsignedPayload || resolved.Prefix != "pre" {
			t.Fatalf("ResolvedConfig = %+v", resolved)
		}
	})

	t.Run("invalid prefix", func(t *testing.T) {
		_, err := New(Config{Bucket: "bucket", Region: "us-east-1", Prefix: "../bad"})
		if !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("New invalid prefix error = %v", err)
		}
	})

	t.Run("partial credentials", func(t *testing.T) {
		for _, cfg := range []Config{
			{Bucket: "bucket", Region: "us-east-1", AccessKeyID: "access"},
			{Bucket: "bucket", Region: "us-east-1", SecretAccessKey: "secret"},
		} {
			if _, err := New(cfg); err == nil {
				t.Fatalf("New(%+v) returned nil error", cfg)
			}
		}
	})

	t.Run("invalid endpoint", func(t *testing.T) {
		for _, endpoint := range []string{"localhost:9000", "ftp://localhost", "http://user@localhost", "http://localhost?query=yes", "http://localhost#fragment"} {
			if _, err := New(Config{Bucket: "bucket", Region: "us-east-1", Endpoint: endpoint}); err == nil {
				t.Fatalf("New endpoint %q returned nil error", endpoint)
			}
		}
	})

	t.Run("canceled setup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewContext(ctx, Config{Bucket: "bucket", Region: "us-east-1"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("NewContext canceled error = %v", err)
		}
	})

	t.Run("normalize endpoint", func(t *testing.T) {
		endpoint, err := normalizeEndpoint("http://localhost:9000/")
		if err != nil {
			t.Fatalf("normalizeEndpoint: %v", err)
		}
		if endpoint != "http://localhost:9000" {
			t.Fatalf("normalizeEndpoint = %q", endpoint)
		}
	})

	t.Run("load aws config", func(t *testing.T) {
		cfg, err := loadAWSConfig(context.Background(), storagecore.ResolvedConfig{
			S3Region:          "us-east-1",
			S3Endpoint:        "http://localhost:9000",
			S3AccessKeyID:     "access",
			S3SecretAccessKey: "secret",
		})
		if err != nil {
			t.Fatalf("loadAWSConfig: %v", err)
		}
		if cfg.Region != "us-east-1" {
			t.Fatalf("aws config region = %q", cfg.Region)
		}
	})

	t.Run("new from disk success and build error", func(t *testing.T) {
		origBuild := buildS3Clients
		t.Cleanup(func() { buildS3Clients = origBuild })

		buildS3Clients = func(cfg aws.Config, resolved storagecore.ResolvedConfig) (s3API, s3PresignAPI) {
			return &fakeS3{}, fakePresign{url: "http://signed"}
		}
		store, err := newFromDiskConfig(context.Background(), storagecore.ResolvedConfig{
			S3Bucket: "bucket",
			S3Region: "us-east-1",
			Prefix:   "pre",
		})
		if err != nil || store == nil {
			t.Fatalf("newFromDiskConfig success err=%v store=%v", err, store)
		}
	})
}

// TestS3ContextCancellation requires every context entry point to fail before issuing a request.
func TestS3ContextCancellation(t *testing.T) {
	d := &driver{bucket: "bucket", prefix: "pre"}
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
	if err := d.MoveContext(ctx, "file.txt", "move.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext error = %v", err)
	}
	if _, err := d.URLContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
}

// TestS3KeyAndPrefixHelpers keeps logical paths confined to the configured key prefix.
func TestS3KeyAndPrefixHelpers(t *testing.T) {
	d := &driver{prefix: "pre"}
	k, err := d.key("file.txt")
	if err != nil {
		t.Fatalf("key error: %v", err)
	}
	if k != "pre/file.txt" {
		t.Fatalf("key got %q", k)
	}
	if got := d.stripPrefix("pre/path/to/file"); got != "path/to/file" {
		t.Fatalf("stripPrefix got %q", got)
	}
}

// TestS3WrapError maps service status and permission errors onto storage sentinels.
func TestS3WrapError(t *testing.T) {
	if err := wrapError(&types.NoSuchKey{}); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for NoSuchKey")
	}
	if err := wrapError(&types.NotFound{}); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for NotFound")
	}
	if !isNotFound(&types.NotFound{}) || !isNotFound(&types.NoSuchKey{}) {
		t.Fatalf("isNotFound should detect known errors")
	}
	if err := wrapError(errors.New("boom")); errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("wrapError should preserve unrelated errors")
	}
	if isNotFound(errors.New("boom")) {
		t.Fatalf("isNotFound should ignore unrelated errors")
	}
}

// TestS3WalkAndURLBranches covers file walks, recursive ordering, signing, and callback failures.
func TestS3WalkAndURLBranches(t *testing.T) {
	t.Run("walk file path", func(t *testing.T) {
		client := &fakeS3{headOK: true}
		d := &driver{
			client:  client,
			presign: fakePresign{url: "http://signed"},
			bucket:  "b",
			prefix:  "pre",
		}

		var got []storagecore.Entry
		if err := d.Walk("file.txt", func(entry storagecore.Entry) error {
			got = append(got, entry)
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		if len(got) != 1 || got[0].Path != "file.txt" || got[0].IsDir {
			t.Fatalf("Walk entries = %+v", got)
		}
	})

	t.Run("walk paginated objects and callback error", func(t *testing.T) {
		client := &fakeS3{
			listSeq: []*s3.ListObjectsV2Output{
				{
					Contents:              []types.Object{{Key: aws.String("pre/folder/file-a.txt"), Size: aws.Int64(1)}},
					IsTruncated:           aws.Bool(true),
					NextContinuationToken: aws.String("next"),
				},
				{
					Contents: []types.Object{{Key: aws.String("pre/file-b.txt"), Size: aws.Int64(2)}},
				},
			},
		}
		d := &driver{client: client, bucket: "b", prefix: "pre"}

		var got []string
		stop := errors.New("stop")
		err := d.Walk("", func(entry storagecore.Entry) error {
			got = append(got, entry.Path)
			if entry.Path == "file-b.txt" {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) {
			t.Fatalf("Walk error = %v", err)
		}
		if len(got) == 0 {
			t.Fatal("Walk returned no entries")
		}
	})

	t.Run("url presign error", func(t *testing.T) {
		d := &driver{
			client:  &fakeS3{},
			presign: fakePresign{err: errors.New("boom")},
			bucket:  "b",
			prefix:  "pre",
		}
		if _, err := d.URL("file.txt"); err == nil {
			t.Fatal("URL returned nil error")
		}
	})

	t.Run("walk head error", func(t *testing.T) {
		d := &driver{
			client: &fakeS3{headErr: errors.New("boom")},
			bucket: "b",
			prefix: "pre",
		}
		if err := d.Walk("file.txt", func(storagecore.Entry) error { return nil }); err == nil {
			t.Fatal("Walk returned nil error")
		}
	})
}

// TestS3MoreBranches covers missing metadata, paginated listing, copying, and path helpers.
func TestS3MoreBranches(t *testing.T) {
	t.Run("stat and exists not found", func(t *testing.T) {
		d := &driver{client: &fakeS3{}, bucket: "b", prefix: "pre"}
		if _, err := d.Stat("missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
			t.Fatalf("Stat missing error = %v", err)
		}
		ok, err := d.Exists("missing.txt")
		if err != nil || ok {
			t.Fatalf("Exists missing = %v err=%v", ok, err)
		}
	})

	t.Run("list pagination and callback error", func(t *testing.T) {
		client := &fakeS3{
			listSeq: []*s3.ListObjectsV2Output{
				{
					CommonPrefixes:        []types.CommonPrefix{{Prefix: aws.String("pre/dir/")}},
					Contents:              []types.Object{{Key: aws.String("pre/dir/file-a.txt"), Size: aws.Int64(1)}},
					IsTruncated:           aws.Bool(true),
					NextContinuationToken: aws.String("next"),
				},
				{
					Contents: []types.Object{
						{Key: aws.String("pre/dir/file-b.txt"), Size: aws.Int64(2)},
						{Key: aws.String("pre/")},
					},
				},
			},
		}
		d := &driver{client: client, bucket: "b", prefix: "pre"}
		entries, err := d.List("")
		if err != nil || len(entries) != 3 {
			t.Fatalf("List pagination entries=%v err=%v", entries, err)
		}
	})

	t.Run("copy and move happy path", func(t *testing.T) {
		client := &fakeS3{headOK: true, getBody: "payload"}
		d := &driver{
			client:  client,
			presign: fakePresign{url: "http://signed"},
			bucket:  "b",
			prefix:  "pre",
		}
		if err := d.Copy("src.txt", "dst.txt"); err != nil {
			t.Fatalf("Copy: %v", err)
		}
		if err := d.Move("src.txt", "moved.txt"); err != nil {
			t.Fatalf("Move: %v", err)
		}
		if err := d.Delete("moved.txt"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})

	t.Run("key and recursive helpers", func(t *testing.T) {
		d := &driver{}
		if _, err := d.key("../bad"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("key invalid error = %v", err)
		}
		if got := d.stripPrefix("plain/path"); got != "plain/path" {
			t.Fatalf("stripPrefix without prefix = %q", got)
		}
		if dirs := recursiveParentDirs("file.txt"); dirs != nil {
			t.Fatalf("recursiveParentDirs file = %v", dirs)
		}
		if dirs := recursiveParentDirs("a/b/file.txt"); len(dirs) != 2 || dirs[0] != "a" || dirs[1] != "a/b" {
			t.Fatalf("recursiveParentDirs nested = %v", dirs)
		}
	})
}

// TestS3DirectoryMarkersAndSafeDelete prevents recursive or ambiguous directory deletion.
func TestS3DirectoryMarkersAndSafeDelete(t *testing.T) {
	t.Run("make directory marker", func(t *testing.T) {
		client := &fakeS3{}
		d := &driver{client: client, bucket: "bucket", prefix: "pre"}
		if err := d.MakeDirContext(context.Background(), "one/two"); err != nil {
			t.Fatalf("MakeDirContext: %v", err)
		}
		if len(client.putInputs) != 1 || aws.ToString(client.putInputs[0].Key) != "pre/one/two/" || aws.ToInt64(client.putInputs[0].ContentLength) != 0 {
			t.Fatalf("PutObject marker = %+v", client.putInputs)
		}
	})

	t.Run("walk directory markers", func(t *testing.T) {
		client := &fakeS3{listOut: &s3.ListObjectsV2Output{Contents: []types.Object{
			{Key: aws.String("pre/zeta/")},
			{Key: aws.String("pre/alpha/child/")},
		}}}
		d := &driver{client: client, bucket: "bucket", prefix: "pre"}
		var paths []string
		if err := d.WalkContext(context.Background(), "", func(entry storagecore.Entry) error {
			if !entry.IsDir {
				t.Fatalf("marker entry is not a directory: %+v", entry)
			}
			paths = append(paths, entry.Path)
			return nil
		}); err != nil {
			t.Fatalf("WalkContext: %v", err)
		}
		if strings.Join(paths, ",") != "alpha,alpha/child,zeta" {
			t.Fatalf("WalkContext marker paths = %v", paths)
		}
	})

	t.Run("walk omits requested ancestors", func(t *testing.T) {
		client := &fakeS3{listOut: &s3.ListObjectsV2Output{Contents: []types.Object{
			{Key: aws.String("pre/move-dir/source/file.txt"), Size: aws.Int64(1)},
		}}}
		d := &driver{client: client, bucket: "bucket", prefix: "pre"}
		var paths []string
		if err := d.WalkContext(context.Background(), "move-dir/source", func(entry storagecore.Entry) error {
			paths = append(paths, entry.Path)
			return nil
		}); err != nil {
			t.Fatalf("WalkContext: %v", err)
		}
		if slices.Contains(paths, "move-dir") || slices.Contains(paths, "move-dir/source") {
			t.Fatalf("Walk returned its root or an ancestor: %v", paths)
		}
	})

	t.Run("delete empty marker", func(t *testing.T) {
		client := &fakeS3{
			headByKey: map[string]*s3.HeadObjectOutput{"pre/folder/": {}},
			listOut:   &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("pre/folder/")}}},
		}
		d := &driver{client: client, bucket: "bucket", prefix: "pre"}
		if err := d.DeleteContext(context.Background(), "folder"); err != nil {
			t.Fatalf("DeleteContext: %v", err)
		}
		if len(client.deleteInputs) != 1 || aws.ToString(client.deleteInputs[0].Key) != "pre/folder/" {
			t.Fatalf("DeleteObject = %+v", client.deleteInputs)
		}
	})

	t.Run("reject nonempty directory", func(t *testing.T) {
		client := &fakeS3{
			headByKey: map[string]*s3.HeadObjectOutput{"pre/folder/": {}},
			listOut: &s3.ListObjectsV2Output{Contents: []types.Object{
				{Key: aws.String("pre/folder/")},
				{Key: aws.String("pre/folder/file.txt")},
			}},
		}
		d := &driver{client: client, bucket: "bucket", prefix: "pre"}
		if err := d.DeleteContext(context.Background(), "folder"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("DeleteContext nonempty error = %v", err)
		}
		if len(client.deleteInputs) != 0 {
			t.Fatal("DeleteObject called for nonempty directory")
		}
	})

	t.Run("reject truncated directory probe", func(t *testing.T) {
		client := &fakeS3{
			headByKey: map[string]*s3.HeadObjectOutput{"pre/folder/": {}},
			listOut: &s3.ListObjectsV2Output{
				Contents:    []types.Object{{Key: aws.String("pre/folder/")}},
				IsTruncated: aws.Bool(true),
			},
		}
		d := &driver{client: client, bucket: "bucket", prefix: "pre"}
		if err := d.DeleteContext(context.Background(), "folder"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("DeleteContext truncated probe error = %v", err)
		}
		if len(client.deleteInputs) != 0 {
			t.Fatal("DeleteObject called after truncated directory probe")
		}
	})
}

// TestS3LogicalRootGuards keeps the configured prefix from becoming a mutable object.
func TestS3LogicalRootGuards(t *testing.T) {
	for _, prefix := range []string{"", "pre"} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			client := &fakeS3{}
			d := &driver{client: client, bucket: "bucket", prefix: prefix}
			if err := d.PutContext(context.Background(), "", []byte("x")); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("PutContext root error = %v", err)
			}
			if err := d.MakeDirContext(context.Background(), ""); err != nil {
				t.Fatalf("MakeDirContext root: %v", err)
			}
			if err := d.DeleteContext(context.Background(), ""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("DeleteContext root error = %v", err)
			}
			if err := d.CopyContext(context.Background(), "", "copy.txt"); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("CopyContext source root error = %v", err)
			}
			if err := d.CopyContext(context.Background(), "source.txt", ""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("CopyContext destination root error = %v", err)
			}
			if err := d.MoveContext(context.Background(), "", "move.txt"); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("MoveContext source root error = %v", err)
			}
			if err := d.MoveContext(context.Background(), "source.txt", ""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("MoveContext destination root error = %v", err)
			}
			entry, err := d.StatContext(context.Background(), "")
			if err != nil || !entry.IsDir || entry.Path != "" {
				t.Fatalf("StatContext root = %+v, %v", entry, err)
			}
			if len(client.putInputs) != 0 || len(client.deleteInputs) != 0 || len(client.getInputs) != 0 || len(client.headInputs) != 0 {
				t.Fatalf("S3 object API called for logical root: put=%d delete=%d get=%d head=%d", len(client.putInputs), len(client.deleteInputs), len(client.getInputs), len(client.headInputs))
			}
		})
	}
}

// TestS3WalkEmptyPrefixedRoot treats an absent configured prefix as an empty logical root.
func TestS3WalkEmptyPrefixedRoot(t *testing.T) {
	client := &fakeS3{}
	d := &driver{client: client, bucket: "bucket", prefix: "pre"}
	called := false
	if err := d.WalkContext(context.Background(), "", func(storagecore.Entry) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("WalkContext empty prefixed root: %v", err)
	}
	if called {
		t.Fatal("WalkContext called callback for empty root")
	}
	if len(client.headInputs) != 0 || len(client.listInputs) != 1 || aws.ToString(client.listInputs[0].Prefix) != "pre/" {
		t.Fatalf("WalkContext calls: head=%d list=%+v", len(client.headInputs), client.listInputs)
	}
}

// TestS3FileDirectoryCollisionsPreferFiles preserves an exact object when marker keys overlap.
func TestS3FileDirectoryCollisionsPreferFiles(t *testing.T) {
	client := &fakeS3{listOut: &s3.ListObjectsV2Output{
		CommonPrefixes: []types.CommonPrefix{{Prefix: aws.String("pre/item/")}},
		Contents: []types.Object{
			{Key: aws.String("pre/item/child.txt"), Size: aws.Int64(2)},
			{Key: aws.String("pre/item"), Size: aws.Int64(1)},
		},
	}}
	d := &driver{client: client, bucket: "bucket", prefix: "pre"}
	entries, err := d.ListContext(context.Background(), "")
	if err != nil {
		t.Fatalf("ListContext: %v", err)
	}
	if len(entries) != 2 || entries[0].Path != "item" || entries[0].IsDir || entries[1].Path != "item/child.txt" {
		t.Fatalf("ListContext collision entries = %+v", entries)
	}

	var walked []storagecore.Entry
	if err := d.WalkContext(context.Background(), "", func(entry storagecore.Entry) error {
		walked = append(walked, entry)
		return nil
	}); err != nil {
		t.Fatalf("WalkContext: %v", err)
	}
	if len(walked) != 2 || walked[0].Path != "item" || walked[0].IsDir || walked[1].Path != "item/child.txt" {
		t.Fatalf("WalkContext collision entries = %+v", walked)
	}
}

// TestS3CopyAndMoveSamePathAreNoOps validates sources without rewriting or deleting them.
func TestS3CopyAndMoveSamePathAreNoOps(t *testing.T) {
	for _, prefix := range []string{"", "pre"} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			key := "file.txt"
			if prefix != "" {
				key = prefix + "/" + key
			}
			client := &fakeS3{
				getBody:   "contents",
				headByKey: map[string]*s3.HeadObjectOutput{key: {ContentLength: aws.Int64(8)}},
			}
			d := &driver{client: client, bucket: "bucket", prefix: prefix}
			if err := d.CopyContext(context.Background(), "folder/../file.txt", "file.txt"); err != nil {
				t.Fatalf("CopyContext same path: %v", err)
			}
			if err := d.MoveContext(context.Background(), "file.txt", "file.txt"); err != nil {
				t.Fatalf("MoveContext same path: %v", err)
			}
			if len(client.getInputs) != 1 || len(client.headInputs) != 1 {
				t.Fatalf("source validation calls: get=%d head=%d", len(client.getInputs), len(client.headInputs))
			}
			if len(client.putInputs)+len(client.deleteInputs) != 0 {
				t.Fatal("S3 mutation API called for same-path operation")
			}
		})
	}

	missing := &driver{client: &fakeS3{getErr: &types.NoSuchKey{}}, bucket: "bucket"}
	if err := missing.CopyContext(context.Background(), "missing.txt", "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("CopyContext missing same path error = %v", err)
	}
	missing.client = &fakeS3{}
	if err := missing.MoveContext(context.Background(), "missing.txt", "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("MoveContext missing same path error = %v", err)
	}
}

// TestS3ErrorClassification distinguishes missing, forbidden, and unrelated service failures.
func TestS3ErrorClassification(t *testing.T) {
	for _, err := range []error{
		&smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"},
		&smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusNotFound}},
			Err:      errors.New("missing"),
		},
	} {
		if wrapped := wrapError(err); !errors.Is(wrapped, storagecore.ErrNotFound) {
			t.Fatalf("not-found error %T classified as %v", err, wrapped)
		}
		if !isNotFound(err) {
			t.Fatalf("isNotFound(%T) = false", err)
		}
	}

	for _, err := range []error{
		&smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"},
		&smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusForbidden}},
			Err:      errors.New("denied"),
		},
	} {
		if wrapped := wrapError(err); !errors.Is(wrapped, storagecore.ErrForbidden) {
			t.Fatalf("forbidden error %T classified as %v", err, wrapped)
		}
	}
}

// TestS3UnsignedPayloadMiddleware applies unsigned payloads only when explicitly configured.
func TestS3UnsignedPayloadMiddleware(t *testing.T) {
	stack := middleware.NewStack("test", smithyhttp.NewStackRequest)
	if err := stack.Finalize.Add(&v4.ComputePayloadSHA256{}, middleware.After); err != nil {
		t.Fatalf("add compute middleware: %v", err)
	}
	if err := useUnsignedPayload(stack); err != nil {
		t.Fatalf("useUnsignedPayload: %v", err)
	}
	middlewareValue, ok := stack.Finalize.Get((&v4.UnsignedPayload{}).ID())
	if !ok {
		t.Fatal("unsigned payload middleware missing")
	}
	if _, ok := middlewareValue.(*v4.UnsignedPayload); !ok {
		t.Fatalf("payload middleware = %T, want *v4.UnsignedPayload", middlewareValue)
	}

	empty := middleware.NewStack("empty", smithyhttp.NewStackRequest)
	if err := useUnsignedPayload(empty); err != nil {
		t.Fatalf("useUnsignedPayload without compute middleware: %v", err)
	}
}

// TestS3GetContextClosesResponseBody verifies response cleanup errors remain observable.
func TestS3GetContextClosesResponseBody(t *testing.T) {
	closeErr := errors.New("close failed")
	body := &trackedReadCloser{reader: strings.NewReader("contents"), closeErr: closeErr}
	d := &driver{client: &fakeS3{getReader: body}, bucket: "bucket"}
	if _, err := d.GetContext(context.Background(), "file.txt"); !errors.Is(err, closeErr) {
		t.Fatalf("GetContext close error = %v", err)
	}
	if !body.closed {
		t.Fatal("GetContext did not close response body")
	}
}

// TestS3GetContextReadFailureStillCloses verifies failed streams are cleaned up.
func TestS3GetContextReadFailureStillCloses(t *testing.T) {
	readErr := errors.New("read failed")
	body := &trackedReadCloser{reader: failingReader{err: readErr}}
	d := &driver{client: &fakeS3{getReader: body}, bucket: "bucket"}
	if _, err := d.GetContext(context.Background(), "file.txt"); !errors.Is(err, readErr) {
		t.Fatalf("GetContext read error = %v", err)
	}
	if !body.closed {
		t.Fatal("GetContext did not close failed response body")
	}
}
