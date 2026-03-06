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
