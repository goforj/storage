//go:build !integration

package ftpstorage

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goftp/server"
)

type memFactory struct {
	root string
}

// NewDriver gives each embedded FTP connection a filesystem-backed test driver.
func (f *memFactory) NewDriver() (server.Driver, error) {
	return &memDriver{root: f.root, perm: server.NewSimplePerm("user", "group")}, nil
}

type memDriver struct {
	root string
	perm server.Perm
}

// Init satisfies the embedded server contract without per-connection state.
func (d *memDriver) Init(*server.Conn) {}

// Stat exposes fixture filesystem metadata through the FTP server interface.
func (d *memDriver) Stat(p string) (server.FileInfo, error) {
	fi, err := os.Stat(d.abs(p))
	if err != nil {
		return nil, err
	}
	return fileInfo{FileInfo: fi}, nil
}

// ChangeDir rejects non-directory fixture paths.
func (d *memDriver) ChangeDir(p string) error {
	fi, err := os.Stat(d.abs(p))
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return os.ErrInvalid
	}
	return nil
}

// ListDir streams immediate fixture children to the embedded FTP server.
func (d *memDriver) ListDir(p string, cb func(server.FileInfo) error) error {
	dir := d.abs(p)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := cb(fileInfo{FileInfo: info}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteDir removes a fixture directory tree for the embedded server.
func (d *memDriver) DeleteDir(p string) error { return os.RemoveAll(d.abs(p)) }

// DeleteFile removes one fixture file for the embedded server.
func (d *memDriver) DeleteFile(p string) error { return os.Remove(d.abs(p)) }

// Rename moves a fixture path within the embedded server root.
func (d *memDriver) Rename(from, to string) error {
	return os.Rename(d.abs(from), d.abs(to))
}

// MakeDir recursively creates a fixture directory for the embedded server.
func (d *memDriver) MakeDir(p string) error {
	return os.MkdirAll(d.abs(p), 0o755)
}

// GetFile opens a fixture download and reports its complete size.
func (d *memDriver) GetFile(p string, _ int64) (int64, io.ReadCloser, error) {
	f, err := os.Open(d.abs(p))
	if err != nil {
		return 0, nil, err
	}
	info, _ := f.Stat()
	return info.Size(), f, nil
}

// PutFile creates missing parents before writing an embedded-server upload.
func (d *memDriver) PutFile(p string, r io.Reader, _ bool) (int64, error) {
	fp := d.abs(p)
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(fp)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

// abs resolves an FTP fixture path beneath its temporary root.
func (d *memDriver) abs(p string) string {
	if p == "" || p == "." {
		return d.root
	}
	return filepath.Join(d.root, p)
}

type fileInfo struct {
	os.FileInfo
}

// Owner supplies the stable username required by server.FileInfo.
func (f fileInfo) Owner() string { return "user" }

// Group supplies the stable group required by server.FileInfo.
func (f fileInfo) Group() string { return "group" }

// TestFTPWithEmbeddedServer verifies a real FTP session can upload and download data.
func TestFTPWithEmbeddedServer(t *testing.T) {
	root := t.TempDir()

	factory := &memFactory{root: root}
	opts := &server.ServerOpts{
		Factory:  factory,
		Port:     pickPort(),
		Hostname: "127.0.0.1",
		Auth:     &server.SimpleAuth{Name: "anonymous", Password: "anonymous"},
	}
	s := server.NewServer(opts)

	go func() {
		_ = s.ListenAndServe()
	}()
	t.Cleanup(func() {
		_ = s.Shutdown()
	})

	// small delay to ensure server is listening
	time.Sleep(200 * time.Millisecond)

	fs, err := New(Config{
		Host:     "127.0.0.1",
		Port:     opts.Port,
		User:     "anonymous",
		Password: "anonymous",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := fs.Put("hello.txt", []byte("world")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := fs.Get("hello.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "world" {
		t.Fatalf("Get = %q", got)
	}
}

// pickPort asks the kernel for an available loopback port for the embedded server.
func pickPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 2222
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
