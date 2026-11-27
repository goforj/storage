//go:build !integration

package rclonestorage

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/goforj/storage/storagecore"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// TestRcloneStorageBuildsLocalAndS3Backends verifies rendered local and S3 remotes construct usable stores.
func TestRcloneStorageBuildsLocalAndS3Backends(t *testing.T) {
	root := t.TempDir()
	remoteRoot := filepath.Join(root, "remote")
	if err := os.MkdirAll(remoteRoot, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}

	// Fake S3 server for exercising the rclone s3 backend.
	fake := gofakes3.New(s3mem.New())
	var sawNativeCopy atomic.Bool
	handler := fake.Server()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Amz-Copy-Source") != "" {
			sawNativeCopy.Store(true)
		}
		handler.ServeHTTP(w, req)
	}))
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
	for _, test := range []struct {
		name string
		disk storagecore.Storage
		want string
	}{
		{"local", localFS, "local"},
		{"S3", s3FS, "s3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.disk.Put("nested/copy.txt", []byte("previous contents")); err != nil {
				t.Fatal(err)
			}
			if err := test.disk.Copy("hello.txt", "nested/copy.txt"); err != nil {
				t.Fatal(err)
			}
			if got, err := test.disk.Get("nested/copy.txt"); err != nil || string(got) != test.want {
				t.Fatalf("read copy = %q, %v", got, err)
			}
		})
	}
	if !sawNativeCopy.Load() {
		t.Fatal("Rclone did not send a server-side S3 copy request")
	}
}
