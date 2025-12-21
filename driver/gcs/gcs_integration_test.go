//go:build integration

package gcsdriver

import (
	"context"
	"strings"
	"testing"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestGCSIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)
	ctx := context.Background()

	endpoint, bucket := filesystemtest.GCSSettings()
	if err := filesystemtest.EnsureGCSBucket(ctx, endpoint, bucket); err != nil {
		t.Fatalf("skipping GCS integration (ensure bucket failed): %v", err)
	}

	cfg := filesystem.Config{
		Default: "gcs",
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"gcs": {
				Driver:             "gcs",
				GCSBucket:          bucket,
				GCSEndpoint:        endpoint,
				GCSCredentialsJSON: "", // fake-gcs-server runs without auth
				Prefix:             "integration",
			},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	fs, err := mgr.Disk("gcs")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	if err := fs.Put(ctx, "file.txt", []byte("hello")); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "not found") || strings.Contains(err.Error(), "404") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "operation not permitted") {
			t.Skipf("skipping GCS integration (put failed): %v", err)
		}
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
