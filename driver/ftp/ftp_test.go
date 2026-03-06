//go:build !integration

package ftpdriver

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goftp/server"

	"github.com/goforj/storage"
	storagetest "github.com/goforj/storage/storagetest"
)

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
	if p == "" || p == "." {
		return d.root
	}
	return filepath.Join(d.root, p)
}

type fileInfo struct {
	os.FileInfo
}

func (f fileInfo) Owner() string { return "user" }
func (f fileInfo) Group() string { return "group" }

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

	cfg := storage.Config{
		Default: "ftp",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"ftp": Config{
				Host:     "127.0.0.1",
				Port:     opts.Port,
				User:     "anonymous",
				Password: "anonymous",
			},
		},
	}

	// small delay to ensure server is listening
	time.Sleep(200 * time.Millisecond)

	mgr, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	fs, err := mgr.Disk("ftp")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	storagetest.RunStorageContractTests(t, fs)
}

func pickPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 2222
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
