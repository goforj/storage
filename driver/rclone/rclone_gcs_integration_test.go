//go:build integration

package rclone

import (
	"context"
	"testing"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestRcloneGCSIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)
	remotes := ensureRcloneConfig(t)
	if remotes.gcsRemote == "" {
		t.Skip("no gcs remote configured (set INTEGRATION_GCS_CREDS_JSON and ensure fake-gcs-server reachable)")
	}

	ctx := context.Background()

	cfg := filesystem.Config{
		Default:          "rclone",
		RcloneConfigData: remotes.inline,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rclone": {Driver: "rclone", Remote: remotes.gcsRemote, Prefix: "integration"},
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

	if err := fs.Put(ctx, "file.txt", []byte("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	data, err := fs.Get(ctx, "file.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data mismatch: %q", data)
	}
}
