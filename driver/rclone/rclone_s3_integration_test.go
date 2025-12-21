//go:build integration

package rclone

import (
	"context"
	"testing"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestRcloneS3IntegrationWithMinio(t *testing.T) {
	filesystemtest.RequireIntegration(t)
	remotes := ensureRcloneConfig(t)
	if remotes.minioRemote == "" {
		t.Skip("no minio remote configured")
	}

	ctx := context.Background()

	cfg := filesystem.Config{
		Default:          "rclone",
		RcloneConfigData: remotes.inline,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rclone": {Driver: "rclone", Remote: remotes.minioRemote, Prefix: "integration"},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	fs, err := mgr.Disk("rclone")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	if err := fs.Put(ctx, "folder/file.txt", []byte("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	data, err := fs.Get(ctx, "folder/file.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data mismatch: %q", data)
	}
	entries, err := fs.List(ctx, "folder")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "folder/file.txt" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}
