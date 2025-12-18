package rclone

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/filesystem"
	"github.com/goforj/filesystem/rcloneconfig"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestRcloneDriverContract(t *testing.T) {
	root := t.TempDir()
	remoteRoot := filepath.Join(root, "remote")
	if err := os.MkdirAll(remoteRoot, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}

	conf := rcloneconfig.MustRenderLocal("localdisk")

	cfg := filesystem.Config{
		Default:          "rc",
		RcloneConfigData: conf,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rc": {
				Driver: "rclone",
				Remote: "localdisk:" + remoteRoot,
				Prefix: "sandbox",
			},
		},
	}

	manager, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	fsys := manager.Default()

	filesystemtest.RunFilesystemContractTests(t, fsys)

	// Ensure same config path can initialize additional managers.
	if _, err := filesystem.New(cfg); err != nil {
		t.Fatalf("expected reuse of config path to succeed: %v", err)
	}

	// Different config path should error due to global process scope.
	_, err = filesystem.New(filesystem.Config{
		Default:          "rc",
		RcloneConfigPath: filepath.Join(root, "other.conf"),
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rc": {
				Driver: "rclone",
				Remote: remoteRoot,
			},
		},
	})
	if err == nil {
		t.Fatalf("expected error when reinitializing with different config path")
	}
}
