package sftpstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/goforj/storage"
	"golang.org/x/crypto/ssh"
)

func TestSFTPConstructorsAndAuth(t *testing.T) {
	if got := (Config{}).DriverName(); got != "sftp" {
		t.Fatalf("DriverName = %q", got)
	}

	t.Run("new missing host", func(t *testing.T) {
		_, err := New(Config{})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("build auth missing credentials", func(t *testing.T) {
		_, err := buildAuth(storage.ResolvedConfig{})
		if err == nil {
			t.Fatal("buildAuth returned nil error")
		}
	})

	t.Run("build auth invalid key", func(t *testing.T) {
		keyPath := t.TempDir() + "/id_ed25519"
		if err := os.WriteFile(keyPath, []byte("invalid"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := buildAuth(storage.ResolvedConfig{SFTPKeyPath: keyPath})
		if err == nil {
			t.Fatal("buildAuth returned nil error")
		}
	})

	t.Run("build host key callback defaults", func(t *testing.T) {
		cb, err := buildHostKeyCallback(storage.ResolvedConfig{})
		if err != nil {
			t.Fatalf("buildHostKeyCallback: %v", err)
		}
		if cb == nil {
			t.Fatal("buildHostKeyCallback returned nil callback")
		}
	})

	t.Run("build auth password", func(t *testing.T) {
		methods, err := buildAuth(storage.ResolvedConfig{SFTPPassword: "secret"})
		if err != nil {
			t.Fatalf("buildAuth password: %v", err)
		}
		if len(methods) != 1 {
			t.Fatalf("buildAuth methods = %d", len(methods))
		}
	})

	t.Run("build host key callback invalid known hosts", func(t *testing.T) {
		_, err := buildHostKeyCallback(storage.ResolvedConfig{SFTPKnownHostsPath: t.TempDir() + "/missing"})
		if err == nil {
			t.Fatal("buildHostKeyCallback returned nil error")
		}
	})

	t.Run("new from disk success and failures", func(t *testing.T) {
		origDial := sshDial
		origNewClient := newSFTPClient
		t.Cleanup(func() {
			sshDial = origDial
			newSFTPClient = origNewClient
		})

		sshDial = func(network, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
			if addr == "bad:22" {
				return nil, errors.New("dial boom")
			}
			return &ssh.Client{}, nil
		}

		newSFTPClient = func(client *ssh.Client) (sftpClient, error) {
			if client == nil {
				return nil, errors.New("nil client")
			}
			return &fakeSFTPClient{}, nil
		}

		store, err := newFromDiskConfig(context.Background(), storage.ResolvedConfig{
			SFTPHost:     "good",
			SFTPPassword: "secret",
			Prefix:       "pre",
		})
		if err != nil || store == nil {
			t.Fatalf("newFromDiskConfig success err=%v store=%v", err, store)
		}

		if _, err := newFromDiskConfig(context.Background(), storage.ResolvedConfig{
			SFTPHost:     "bad",
			SFTPPassword: "secret",
		}); err == nil {
			t.Fatal("newFromDiskConfig dial returned nil error")
		}

		newSFTPClient = func(*ssh.Client) (sftpClient, error) { return nil, errors.New("client boom") }
		if _, err := newFromDiskConfig(context.Background(), storage.ResolvedConfig{
			SFTPHost:     "good",
			SFTPPassword: "secret",
		}); err == nil {
			t.Fatal("newFromDiskConfig client returned nil error")
		}

		newSFTPClient = func(*ssh.Client) (sftpClient, error) { return &fakeSFTPClient{}, nil }
		if _, err := newFromDiskConfig(context.Background(), storage.ResolvedConfig{
			SFTPHost:     "good",
			SFTPPassword: "secret",
			Prefix:       "../bad",
		}); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("newFromDiskConfig invalid prefix error = %v", err)
		}
	})
}

func TestSFTPPrefixHelpers(t *testing.T) {
	d := &driver{prefix: "pre"}
	fp, err := d.fullPath("file.txt")
	if err != nil {
		t.Fatalf("fullPath err: %v", err)
	}
	if fp != "pre/file.txt" {
		t.Fatalf("fullPath got %q", fp)
	}
	if got := d.stripPrefix("pre/path/to"); got != "path/to" {
		t.Fatalf("stripPrefix got %q", got)
	}
}

func TestSFTPWrapError(t *testing.T) {
	if err := wrapError(os.ErrNotExist); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound")
	}
	if err := wrapError(os.ErrPermission); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden")
	}
	if err := wrapError(errors.New("other")); errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("wrapError should preserve unrelated errors")
	}
}

func TestSFTPContextCancellation(t *testing.T) {
	d := &driver{prefix: "pre"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := d.GetContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetContext error = %v", err)
	}
	if err := d.PutContext(ctx, "file.txt", []byte("hello")); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext error = %v", err)
	}
	if err := d.DeleteContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext error = %v", err)
	}
	if _, err := d.ExistsContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExistsContext error = %v", err)
	}
	if _, err := d.ListContext(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext error = %v", err)
	}
	if err := d.WalkContext(ctx, "", func(storage.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext error = %v", err)
	}
	if err := d.CopyContext(ctx, "file.txt", "copy.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext error = %v", err)
	}
	if err := d.MoveContext(ctx, "file.txt", "moved.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext error = %v", err)
	}
	if _, err := d.URL("file.txt"); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("URL error = %v", err)
	}
}

func TestSFTPResolvedConfigAndHelpers(t *testing.T) {
	cfg := Config{
		Host:                  "127.0.0.1",
		Port:                  2022,
		User:                  "demo",
		Password:              "secret",
		KeyPath:               "/tmp/key",
		KnownHostsPath:        "/tmp/known_hosts",
		InsecureIgnoreHostKey: true,
		Prefix:                "pre",
	}
	resolved := cfg.ResolvedConfig()
	if resolved.Driver != "sftp" || resolved.SFTPHost != "127.0.0.1" || resolved.Prefix != "pre" || !resolved.SFTPInsecureIgnoreHostKey {
		t.Fatalf("ResolvedConfig = %+v", resolved)
	}

	d := &driver{}
	if got := d.stripPrefix("plain/path"); got != "plain/path" {
		t.Fatalf("stripPrefix without prefix = %q", got)
	}
	if _, err := d.fullPath("../bad"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("fullPath invalid error = %v", err)
	}
}

type fakeSFTPClient struct {
	openData    string
	openReader  io.ReadCloser
	openErr     error
	openFile    *fakeWriteCloser
	openFileErr error
	mkdirErr    error
	removeErr   error
	statInfo    os.FileInfo
	statErr     error
	readDir     []os.FileInfo
	readDirErr  error
}

func (f *fakeSFTPClient) Open(string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.openReader != nil {
		return f.openReader, nil
	}
	return io.NopCloser(bytes.NewBufferString(f.openData)), nil
}

func (f *fakeSFTPClient) OpenFile(string, int) (io.WriteCloser, error) {
	if f.openFileErr != nil {
		return nil, f.openFileErr
	}
	if f.openFile == nil {
		f.openFile = &fakeWriteCloser{}
	}
	return f.openFile, nil
}

func (f *fakeSFTPClient) MkdirAll(string) error                 { return f.mkdirErr }
func (f *fakeSFTPClient) Remove(string) error                   { return f.removeErr }
func (f *fakeSFTPClient) Stat(string) (os.FileInfo, error)      { return f.statInfo, f.statErr }
func (f *fakeSFTPClient) ReadDir(string) ([]os.FileInfo, error) { return f.readDir, f.readDirErr }
func (f *fakeSFTPClient) Close() error                          { return nil }

type fakeWriteCloser struct {
	buf      bytes.Buffer
	writeErr error
	closeErr error
}

func (f *fakeWriteCloser) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.buf.Write(p)
}

func (f *fakeWriteCloser) Close() error { return f.closeErr }

type fakeFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (f fakeFileInfo) Name() string { return f.name }
func (f fakeFileInfo) Size() int64  { return f.size }
func (f fakeFileInfo) Mode() os.FileMode {
	if f.isDir {
		return os.ModeDir
	}
	return 0
}
func (f fakeFileInfo) ModTime() time.Time { return time.Now() }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() interface{}   { return nil }

func TestSFTPFakeBackedOperations(t *testing.T) {
	client := &fakeSFTPClient{
		openData: "hello",
		statInfo: fakeFileInfo{name: "file.txt", size: 5},
		readDir: []os.FileInfo{
			fakeFileInfo{name: "file.txt", size: 5},
			fakeFileInfo{name: "folder", isDir: true},
		},
	}
	d := &driver{client: client, prefix: "pre"}

	data, err := d.Get("file.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("Get = %q err=%v", data, err)
	}
	if err := d.Put("folder/file.txt", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := client.openFile.buf.String(); got != "payload" {
		t.Fatalf("written payload = %q", got)
	}
	if err := d.Delete("file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entry, err := d.Stat("file.txt")
	if err != nil || entry.Path != "file.txt" || entry.Size != 5 || entry.IsDir {
		t.Fatalf("Stat = %+v err=%v", entry, err)
	}
	exists, err := d.Exists("file.txt")
	if err != nil || !exists {
		t.Fatalf("Exists = %v err=%v", exists, err)
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List = %+v err=%v", entries, err)
	}
}

func TestSFTPFakeWalkAndErrors(t *testing.T) {
	t.Run("walk file path", func(t *testing.T) {
		d := &driver{
			client: &fakeSFTPClient{statInfo: fakeFileInfo{name: "file.txt", size: 4}},
			prefix: "pre",
		}
		var got []storage.Entry
		if err := d.Walk("file.txt", func(entry storage.Entry) error {
			got = append(got, entry)
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		if len(got) != 1 || got[0].Path != "file.txt" {
			t.Fatalf("Walk entries = %+v", got)
		}
	})

	t.Run("walk recursive and callback error", func(t *testing.T) {
		client := &fakeSFTPClient{
			statInfo: fakeFileInfo{name: "folder", isDir: true},
			readDir: []os.FileInfo{
				fakeFileInfo{name: "file-a.txt", size: 1},
				fakeFileInfo{name: "sub", isDir: true},
			},
		}
		d := &driver{client: client, prefix: "pre"}
		stop := errors.New("stop")
		err := d.Walk("folder", func(entry storage.Entry) error {
			if entry.Path == "folder/sub" {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) {
			t.Fatalf("Walk callback error = %v", err)
		}
	})

	t.Run("operation errors", func(t *testing.T) {
		d := &driver{
			client: &fakeSFTPClient{
				openErr:     os.ErrNotExist,
				mkdirErr:    os.ErrPermission,
				removeErr:   os.ErrPermission,
				statErr:     os.ErrPermission,
				readDirErr:  os.ErrPermission,
				openFileErr: os.ErrPermission,
			},
			prefix: "pre",
		}
		if _, err := d.Get("file.txt"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get error = %v", err)
		}
		if err := d.Put("file.txt", []byte("x")); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("Put error = %v", err)
		}
		if err := d.Delete("file.txt"); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("Delete error = %v", err)
		}
		if _, err := d.Stat("file.txt"); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("Stat error = %v", err)
		}
		if _, err := d.Exists("file.txt"); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("Exists error = %v", err)
		}
		if _, err := d.List("file.txt"); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("List error = %v", err)
		}
	})

	t.Run("exists false for dir and missing", func(t *testing.T) {
		d := &driver{client: &fakeSFTPClient{statInfo: fakeFileInfo{name: "dir", isDir: true}}, prefix: "pre"}
		ok, err := d.Exists("dir")
		if err != nil || ok {
			t.Fatalf("Exists dir = %v err=%v", ok, err)
		}
		d = &driver{client: &fakeSFTPClient{statErr: os.ErrNotExist}, prefix: "pre"}
		ok, err = d.Exists("missing")
		if err != nil || ok {
			t.Fatalf("Exists missing = %v err=%v", ok, err)
		}
	})

	t.Run("read and write body failures", func(t *testing.T) {
		d := &driver{
			client: &fakeSFTPClient{
				openReader: &failingReadCloser{},
				openFile:   &fakeWriteCloser{writeErr: errors.New("write boom")},
			},
			prefix: "pre",
		}
		if _, err := d.Get("file.txt"); err == nil {
			t.Fatal("Get returned nil error")
		}
		if err := d.Put("file.txt", []byte("x")); err == nil {
			t.Fatal("Put returned nil error")
		}
	})
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("read boom") }
func (failingReadCloser) Close() error             { return nil }
