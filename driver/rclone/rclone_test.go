//go:build !integration

package rclone

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

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

	// Fake S3 server for exercising the rclone s3 backend.
	fake := gofakes3.New(s3mem.New())
	httpServer := httptest.NewServer(fake.Server())
	defer httpServer.Close()

	localConf := rcloneconfig.MustRenderLocal("localdisk")
	s3Conf := rcloneconfig.MustRenderS3(rcloneconfig.S3Remote{
		Name:               "s3fake",
		Endpoint:           httpServer.URL,
		Region:             "us-east-1",
		AccessKeyID:        "access",
		SecretAccessKey:    "secret",
		PathStyle:          true,
		UseUnsignedPayload: true, // simplify fake server compat (no seek required)
	})

	cfg := filesystem.Config{
		Default:          "local",
		RcloneConfigData: localConf + "\n" + s3Conf,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"local": {
				Driver: "rclone",
				Remote: "localdisk:" + remoteRoot,
				Prefix: "sandbox",
			},
			"s3": {
				Driver: "rclone",
				Remote: "s3fake:bucket",
				Prefix: "sandbox",
			},
		},
	}

	manager, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}

	localFS, err := manager.Disk("local")
	if err != nil {
		t.Fatalf("local disk: %v", err)
	}
	s3FS, err := manager.Disk("s3")
	if err != nil {
		t.Fatalf("s3 disk: %v", err)
	}

	filesystemtest.RunFilesystemContractTests(t, localFS)
	filesystemtest.RunFilesystemContractTests(t, s3FS)
}
