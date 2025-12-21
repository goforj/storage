//go:build integration

package rclone

import (
	"context"
	"testing"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestRcloneFTPIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)

	host := filesystemtest.GetenvDefault("INTEGRATION_FTP_HOST", "127.0.0.1")
	port := filesystemtest.GetenvDefault("INTEGRATION_FTP_PORT", "2121")

	remotes := ensureRcloneConfig(t)
	if remotes.ftpRemote == "" {
		t.Fatalf("ftp remote not configured (host %s port %s)", host, port)
	}

	cfg := filesystem.Config{
		Default:          "rclone",
		RcloneConfigData: remotes.inline,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rclone": {Driver: "rclone", Remote: remotes.ftpRemote, Prefix: "integration"},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("rclone ftp integration manager init failed: %v", err)
	}
	fs, err := mgr.Disk("rclone")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	ctx := context.Background()
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
}
