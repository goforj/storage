//go:build !integration

package rclonestorage

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func TestRcloneStorageBuildsLocalAndS3Backends(t *testing.T) {
	root := t.TempDir()
	remoteRoot := filepath.Join(root, "remote")
	if err := os.MkdirAll(remoteRoot, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}

	// Fake S3 server for exercising the rclone s3 backend.
	fake := gofakes3.New(s3mem.New())
	httpServer := httptest.NewServer(fake.Server())
	defer httpServer.Close()

	localConf := MustRenderLocal(LocalRemote{Name: "localdisk"})
	s3Conf := MustRenderS3(S3Remote{
		Name:               "s3fake",
		Endpoint:           httpServer.URL,
		Region:             "us-east-1",
		AccessKeyID:        "access",
		SecretAccessKey:    "secret",
		PathStyle:          true,
		UseUnsignedPayload: true, // simplify fake server compat (no seek required)
	})

	localFS, err := New(Config{
		Remote:           "localdisk:" + remoteRoot,
		Prefix:           "sandbox",
		RcloneConfigData: localConf + "\n" + s3Conf,
	})
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	s3FS, err := New(Config{
		Remote:           "s3fake:bucket",
		Prefix:           "sandbox",
		RcloneConfigData: localConf + "\n" + s3Conf,
	})
	if err != nil {
		t.Fatalf("New s3: %v", err)
	}

	if err := localFS.Put("hello.txt", []byte("local")); err != nil {
		t.Fatalf("local Put: %v", err)
	}
	if err := s3FS.Put("hello.txt", []byte("s3")); err != nil {
		t.Fatalf("s3 Put: %v", err)
	}
}
