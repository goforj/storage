//go:build integration

package s3driver

import (
	"context"
	"testing"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestS3IntegrationWithMinio(t *testing.T) {
	filesystemtest.RequireIntegration(t)

	endpoint, region, access, secret, bucket := filesystemtest.S3Settings()
	if err := filesystemtest.EnsureS3Bucket(context.Background(), endpoint, region, access, secret, bucket); err != nil {
		t.Fatalf("ensure bucket failed: %v", err)
	}

	cfg := filesystem.Config{
		Default: "s3",
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"s3": {
				Driver:            "s3",
				S3Bucket:          bucket,
				S3Region:          region,
				S3Endpoint:        endpoint,
				S3AccessKeyID:     access,
				S3SecretAccessKey: secret,
				S3UsePathStyle:    true,
				Prefix:            "integration",
			},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	fs, err := mgr.Disk("s3")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	ctx := context.Background()
	if err := fs.Put(ctx, "folder/file.txt", []byte("hello")); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	data, err := fs.Get(ctx, "folder/file.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected data: %q", data)
	}
	exists, err := fs.Exists(ctx, "folder/file.txt")
	if err != nil || !exists {
		t.Fatalf("exists: %v exists %v", err, exists)
	}
	if _, err := fs.URL(ctx, "folder/file.txt"); err != nil {
		t.Fatalf("url: %v", err)
	}

	entries, err := fs.List(ctx, "folder")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "folder/file.txt" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}
