//go:build integration

package gcsdriver

import (
	"context"
	"testing"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestGCSIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)
	ctx := context.Background()

	bucket := "gcs-integration"
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		NoListener: true,
	})
	if err != nil {
		t.Fatalf("start fake gcs: %v", err)
	}
	defer server.Stop()
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: bucket})
	fs := &Driver{client: server.Client(), bucket: bucket, prefix: "integration"}

	if err := fs.Put(ctx, "file.txt", []byte("hello")); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	data, err := fs.Get(ctx, "file.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data mismatch: %q", data)
	}
	exists, err := fs.Exists(ctx, "file.txt")
	if err != nil || !exists {
		t.Fatalf("exists: %v exists %v", err, exists)
	}
	entries, err := fs.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "file.txt" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}
