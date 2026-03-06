package s3storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/goforj/storage"
)

type fakeS3 struct {
	getErr  error
	putErr  error
	delErr  error
	headErr error
	listErr error
	listOut *s3.ListObjectsV2Output
	listSeq []*s3.ListObjectsV2Output
	headOK  bool
	getBody string
}

func (f *fakeS3) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(f.getBody))}, nil
}
func (f *fakeS3) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, f.putErr
}
func (f *fakeS3) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, f.delErr
}
func (f *fakeS3) HeadObject(ctx context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.headOK {
		return &s3.HeadObjectOutput{}, nil
	}
	return nil, &types.NotFound{}
}
func (f *fakeS3) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
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

func (f fakePresign) PresignGetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &v4.PresignedHTTPRequest{URL: f.url}, nil
}

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

	// list
	client.listOut = &s3.ListObjectsV2Output{
		CommonPrefixes: []types.CommonPrefix{{Prefix: aws.String("pre/dir/")}},
		Contents:       []types.Object{{Key: aws.String("pre/dir/file.txt"), Size: aws.Int64(5)}},
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List err %v entries %v", err, entries)
	}
}

func TestS3Constructors(t *testing.T) {
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
}

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
	if err := d.WalkContext(ctx, "", func(storage.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext error = %v", err)
	}
	if _, err := d.URLContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
}

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

func TestS3WrapError(t *testing.T) {
	if err := wrapError(&types.NoSuchKey{}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for NoSuchKey")
	}
	if err := wrapError(&types.NotFound{}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for NotFound")
	}
	if !isNotFound(&types.NotFound{}) || !isNotFound(&types.NoSuchKey{}) {
		t.Fatalf("isNotFound should detect known errors")
	}
}

func TestS3WalkAndURLBranches(t *testing.T) {
	t.Run("walk file path", func(t *testing.T) {
		client := &fakeS3{headOK: true}
		d := &driver{
			client:  client,
			presign: fakePresign{url: "http://signed"},
			bucket:  "b",
			prefix:  "pre",
		}

		var got []storage.Entry
		if err := d.Walk("file.txt", func(entry storage.Entry) error {
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
					Contents: []types.Object{{Key: aws.String("pre/folder/file-a.txt"), Size: aws.Int64(1)}},
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
		err := d.Walk("", func(entry storage.Entry) error {
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
}
