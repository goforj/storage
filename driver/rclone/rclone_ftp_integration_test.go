//go:build integration

package rclone

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goftp/server"
	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestRcloneFTPIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)

	host, port, user, pass, ok := startEmbeddedFTP(t)
	if !ok {
		t.Skip("unable to start embedded ftp server")
	}
	inline := fmt.Sprintf(`
[ftpbackend]
type = ftp
host = %s
port = %d
user = %s
pass = %s
`, host, port, user, obscure.MustObscure(pass))

	if !setRcloneConfigData(inline) {
		t.Skip("rclone already initialized with different inline config")
	}

	cfg := filesystem.Config{
		Default:          "rclone",
		RcloneConfigData: inline,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rclone": {Driver: "rclone", Remote: "ftpbackend:/", Prefix: "integration"},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") || strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("rclone ftp integration skipped (cannot connect): %v", err)
		}
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

// embedded FTP server setup (mirrors the driver/ftp integration harness)
func startEmbeddedFTP(t *testing.T) (host string, port int, user, pass string, ok bool) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", 0, "", "", false
	}
	defer l.Close()
	port = l.Addr().(*net.TCPAddr).Port

	root := t.TempDir()
	host = "127.0.0.1"
	user = "ftpuser"
	pass = "ftppass"

	factory := &memFactory{root: root}
	opts := &server.ServerOpts{
		Factory:  factory,
		Port:     port,
		Hostname: host,
		Auth:     &server.SimpleAuth{Name: user, Password: pass},
	}
	s := server.NewServer(opts)
	errCh := make(chan error, 1)
	go func() { errCh <- s.ListenAndServe() }()
	t.Cleanup(func() { _ = s.Shutdown() })
	addr := fmt.Sprintf("%s:%d", host, port)
	for i := 0; i < 10; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				return "", 0, "", "", false
			}
		default:
		}
		if conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			return host, port, user, pass, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", 0, "", "", false
}

// the minimal driver implementation is duplicated under integration build tag
type memFactory struct {
	root string
}

func (f *memFactory) NewDriver() (server.Driver, error) {
	return &memDriver{root: f.root, perm: server.NewSimplePerm("user", "group")}, nil
}

type memDriver struct {
	root string
	perm server.Perm
}

func (d *memDriver) Init(*server.Conn) {}

func (d *memDriver) Stat(p string) (server.FileInfo, error) {
	fi, err := os.Stat(d.abs(p))
	if err != nil {
		return nil, err
	}
	return fileInfo{FileInfo: fi}, nil
}

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

func (d *memDriver) DeleteDir(p string) error  { return os.RemoveAll(d.abs(p)) }
func (d *memDriver) DeleteFile(p string) error { return os.Remove(d.abs(p)) }
func (d *memDriver) Rename(from, to string) error {
	return os.Rename(d.abs(from), d.abs(to))
}
func (d *memDriver) MakeDir(p string) error {
	return os.MkdirAll(d.abs(p), 0o755)
}
func (d *memDriver) GetFile(p string, _ int64) (int64, io.ReadCloser, error) {
	f, err := os.Open(d.abs(p))
	if err != nil {
		return 0, nil, err
	}
	info, _ := f.Stat()
	return info.Size(), f, nil
}
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

func (d *memDriver) abs(p string) string {
	if p == "" || p == "." || p == "/" {
		return d.root
	}
	return filepath.Join(d.root, p)
}

type fileInfo struct {
	os.FileInfo
}

func (f fileInfo) Owner() string { return "user" }
func (f fileInfo) Group() string { return "group" }
