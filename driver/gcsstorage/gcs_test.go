package gcsstorage

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/goforj/storage"
)

func TestGCSStorageWithFakeServer(t *testing.T) {
	host := "127.0.0.1"
	port := uint16(pickPort(t))
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme:     "http",
		Host:       host,
		Port:       port,
		PublicHost: fmt.Sprintf("%s:%d", host, port),
	})
	if err != nil {
		t.Fatalf("start fake gcs server: %v", err)
	}
	defer server.Stop()

	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: "gcs-test"})

	disk, err := New(Config{
		Bucket:   "gcs-test",
		Endpoint: server.URL(),
		Prefix:   "itest",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := disk.Put("docs/readme.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := disk.Get("docs/readme.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Get got %q", got)
	}

	exists, err := disk.Exists("docs/readme.txt")
	if err != nil || !exists {
		t.Fatalf("Exists err=%v exists=%v", err, exists)
	}

	if err := disk.Put("docs/nested/file.txt", []byte("nested")); err != nil {
		t.Fatalf("Put nested: %v", err)
	}

	entries, err := disk.List("docs")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("List entries = %+v", entries)
	}

	var walked []string
	if err := disk.Walk("docs", func(entry storage.Entry) error {
		walked = append(walked, entry.Path)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(walked) < 2 {
		t.Fatalf("Walk entries = %v", walked)
	}

	url, err := disk.URL("docs/readme.txt")
	if !errors.Is(err, storage.ErrUnsupported) || url != "" {
		t.Fatalf("URL err=%v url=%q", err, url)
	}

	if err := disk.Delete("docs/readme.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	exists, err = disk.Exists("docs/readme.txt")
	if err != nil || exists {
		t.Fatalf("Exists after delete err=%v exists=%v", err, exists)
	}
}

func pickPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
