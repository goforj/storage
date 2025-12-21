//go:build integration

package rclone

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestRcloneSFTPIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)

	host := filesystemtest.GetenvDefault("INTEGRATION_SFTP_HOST", "127.0.0.1")
	port := filesystemtest.GetenvDefault("INTEGRATION_SFTP_PORT", "2222")

	addr := fmt.Sprintf("%s:%s", host, port)
	if !filesystemtest.Reachable(addr) {
		t.Fatalf("sftp endpoint not reachable at %s", addr)
	}

	remotes := ensureRcloneConfig(t)
	if remotes.sftpRemote == "" {
		t.Fatalf("sftp remote not configured; ensure SFTP service reachable at %s", addr)
	}

	cfg := filesystem.Config{
		Default:          "rclone",
		RcloneConfigData: remotes.inline,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rclone": {Driver: "rclone", Remote: remotes.sftpRemote, Prefix: "integration"},
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

	ctx := context.Background()
	if err := fs.Put(ctx, "folder/file.txt", []byte("hello")); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
			t.Skipf("skipping sftp integration (permission denied): %v", err)
		}
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
