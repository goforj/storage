//go:build integration

package all

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/goforj/storage"
	ftpstorage "github.com/goforj/storage/driver/ftpstorage"
	gcsstorage "github.com/goforj/storage/driver/gcsstorage"
	localstorage "github.com/goforj/storage/driver/localstorage"
	rclonestorage "github.com/goforj/storage/driver/rclonestorage"
	s3storage "github.com/goforj/storage/driver/s3storage"
	sftpstorage "github.com/goforj/storage/driver/sftpstorage"
	"github.com/goforj/storage/storagetest"
	"github.com/goftp/server"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type storageFactory struct {
	name string
	new  func(t *testing.T) (storage.Storage, func())
}

func TestStorageContract_AllDrivers(t *testing.T) {
	var fixtures []storageFactory

	if integrationDriverEnabled("local") {
		fixtures = append(fixtures, storageFactory{
			name: "local",
			new: func(t *testing.T) (storage.Storage, func()) {
				t.Helper()
				store, err := storage.Build(localstorage.Config{Remote: t.TempDir(), Prefix: "itest"})
				if err != nil {
					t.Fatalf("build local storage: %v", err)
				}
				return store, func() {}
			},
		})
	}

	if integrationDriverEnabled("gcs") {
		fixtures = append(fixtures, storageFactory{
			name: "gcs",
			new: func(t *testing.T) (storage.Storage, func()) {
				t.Helper()
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
				server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: "storage-itest"})
				store, err := storage.Build(gcsstorage.Config{
					Bucket:   "storage-itest",
					Endpoint: server.URL(),
					Prefix:   "itest",
				})
				if err != nil {
					server.Stop()
					t.Fatalf("build gcs storage: %v", err)
				}
				return store, func() {
					server.Stop()
				}
			},
		})
	}

	if integrationDriverEnabled("rclone_local") || integrationDriverEnabled("rclone") {
		fixtures = append(fixtures, storageFactory{
			name: "rclone_local",
			new: func(t *testing.T) (storage.Storage, func()) {
				t.Helper()
				root := t.TempDir()
				conf, err := rclonestorage.RenderLocal(rclonestorage.LocalRemote{Name: "localdisk"})
				if err != nil {
					t.Fatalf("render rclone local config: %v", err)
				}
				store, err := rclonestorage.New(rclonestorage.Config{
					Remote:           "localdisk:" + root,
					Prefix:           "itest",
					RcloneConfigData: conf,
				})
				if err != nil {
					t.Fatalf("build rclone local storage: %v", err)
				}
				return store, func() {}
			},
		})
	}

	if integrationDriverEnabled("ftp") {
		fixtures = append(fixtures, storageFactory{
			name: "ftp",
			new: func(t *testing.T) (storage.Storage, func()) {
				t.Helper()
				host := "127.0.0.1"
				root := t.TempDir()
				port := pickPort(t)
				srv := startEmbeddedFTPServer(t, host, port, root)
				store, err := storage.Build(ftpstorage.Config{
					Host:     host,
					Port:     port,
					User:     "ftpuser",
					Password: "ftppass",
					Prefix:   "integration/itest",
				})
				if err != nil {
					_ = srv.Shutdown()
					t.Fatalf("build ftp storage: %v", err)
				}
				return store, func() {
					_ = srv.Shutdown()
				}
			},
		})
	}

	if integrationDriverEnabled("s3") {
		fixtures = append(fixtures, storageFactory{
			name: "s3",
			new: func(t *testing.T) (storage.Storage, func()) {
				t.Helper()
				ctx := context.Background()
				container, endpoint := startMinioContainer(t, ctx)
				if err := storagetest.EnsureS3Bucket(ctx, endpoint, "us-east-1", "minioadmin", "minioadmin", "storage-itest"); err != nil {
					shutdownContainer(t, container)
					t.Fatalf("create minio bucket: %v", err)
				}
				store, err := storage.Build(s3storage.Config{
					Bucket:          "storage-itest",
					Region:          "us-east-1",
					Endpoint:        endpoint,
					AccessKeyID:     "minioadmin",
					SecretAccessKey: "minioadmin",
					UsePathStyle:    true,
					Prefix:          "itest",
				})
				if err != nil {
					shutdownContainer(t, container)
					t.Fatalf("build s3 storage: %v", err)
				}
				return store, func() {
					shutdownContainer(t, container)
				}
			},
		})
	}

	if integrationDriverEnabled("sftp") {
		fixtures = append(fixtures, storageFactory{
			name: "sftp",
			new: func(t *testing.T) (storage.Storage, func()) {
				t.Helper()
				ctx := context.Background()
				container, host, port := startSFTPContainer(t, ctx)
				store, err := storage.Build(sftpstorage.Config{
					Host:                  host,
					Port:                  port,
					User:                  "storage",
					Password:              "storage",
					InsecureIgnoreHostKey: true,
					Prefix:                "upload/itest",
				})
				if err != nil {
					shutdownContainer(t, container)
					t.Fatalf("build sftp storage: %v", err)
				}
				return store, func() {
					shutdownContainer(t, container)
				}
			},
		})
	}

	if len(fixtures) == 0 {
		t.Skip("no integration drivers selected")
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			store, cleanup := fx.new(t)
			t.Cleanup(cleanup)
			storagetest.RunStorageContractTests(t, store)
			verifyWalk(t, store)
		})
	}
}

func verifyWalk(t *testing.T, store storage.Storage) {
	t.Helper()

	var walked []string
	err := store.Walk("", func(entry storage.Entry) error {
		walked = append(walked, entry.Path)
		return nil
	})
	if err != nil {
		if errors.Is(err, storage.ErrUnsupported) {
			t.Skip("Walk not supported; skipping")
		}
		t.Fatalf("Walk: %v", err)
	}
	if len(walked) == 0 {
		t.Fatalf("Walk returned no entries")
	}
	if !containsPath(walked, "folder1/fileA.txt") {
		t.Fatalf("Walk missing expected object, got %v", walked)
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want || strings.HasSuffix(path, "/"+want) {
			return true
		}
	}
	return false
}

func startMinioContainer(t *testing.T, ctx context.Context) (testcontainers.Container, string) {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        "minio/minio:latest",
		Env:          map[string]string{"MINIO_ROOT_USER": "minioadmin", "MINIO_ROOT_PASSWORD": "minioadmin"},
		ExposedPorts: []string{"9000/tcp"},
		Cmd:          []string{"server", "/data"},
		WaitingFor:   wait.ForLog("API:").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start minio container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		shutdownContainer(t, container)
		t.Fatalf("minio host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		shutdownContainer(t, container)
		t.Fatalf("minio mapped port: %v", err)
	}
	return container, "http://" + host + ":" + port.Port()
}

func startSFTPContainer(t *testing.T, ctx context.Context) (testcontainers.Container, string, int) {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        "atmoz/sftp:latest",
		ExposedPorts: []string{"22/tcp"},
		Cmd:          []string{"storage:storage:::upload"},
		WaitingFor:   wait.ForListeningPort("22/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start sftp container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		shutdownContainer(t, container)
		t.Fatalf("sftp host: %v", err)
	}
	port, err := container.MappedPort(ctx, "22/tcp")
	if err != nil {
		shutdownContainer(t, container)
		t.Fatalf("sftp mapped port: %v", err)
	}
	return container, host, port.Int()
}

func shutdownContainer(t *testing.T, container testcontainers.Container) {
	t.Helper()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = container.Terminate(shutdownCtx)
}

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
	entries, err := os.ReadDir(d.abs(p))
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

func pickPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startEmbeddedFTPServer(t *testing.T, host string, port int, root string) *server.Server {
	t.Helper()
	srv := server.NewServer(&server.ServerOpts{
		Factory:  &memFactory{root: root},
		Port:     port,
		Hostname: host,
		Auth:     &server.SimpleAuth{Name: "ftpuser", Password: "ftppass"},
	})
	go func() { _ = srv.ListenAndServe() }()
	time.Sleep(200 * time.Millisecond)
	return srv
}
