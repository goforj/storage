package sftpstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/goforj/storage/storagecore"
	"golang.org/x/crypto/ssh"
)

// TestSFTPConstructorsAndAuth verifies required settings, secure host defaults, and dial cleanup.
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
		_, err := buildAuth(storagecore.ResolvedConfig{})
		if err == nil {
			t.Fatal("buildAuth returned nil error")
		}
	})

	t.Run("build auth invalid key", func(t *testing.T) {
		keyPath := t.TempDir() + "/id_ed25519"
		if err := os.WriteFile(keyPath, []byte("invalid"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := buildAuth(storagecore.ResolvedConfig{SFTPKeyPath: keyPath})
		if err == nil {
			t.Fatal("buildAuth returned nil error")
		}
	})

	t.Run("build host key callback defaults", func(t *testing.T) {
		cb, err := buildHostKeyCallback(storagecore.ResolvedConfig{})
		if err == nil || cb != nil {
			t.Fatalf("buildHostKeyCallback = %v, %v; want secure-default error", cb, err)
		}
	})

	t.Run("build auth password", func(t *testing.T) {
		methods, err := buildAuth(storagecore.ResolvedConfig{SFTPPassword: "secret"})
		if err != nil {
			t.Fatalf("buildAuth password: %v", err)
		}
		if len(methods) != 1 {
			t.Fatalf("buildAuth methods = %d", len(methods))
		}
	})

	t.Run("build host key callback invalid known hosts", func(t *testing.T) {
		_, err := buildHostKeyCallback(storagecore.ResolvedConfig{SFTPKnownHostsPath: t.TempDir() + "/missing"})
		if err == nil {
			t.Fatal("buildHostKeyCallback returned nil error")
		}
	})

	t.Run("new from disk success and failures", func(t *testing.T) {
		origDial := sshDial
		origNewClient := newSFTPClient
		origCloseSSHClient := closeSSHClient
		t.Cleanup(func() {
			sshDial = origDial
			newSFTPClient = origNewClient
			closeSSHClient = origCloseSSHClient
		})
		closeSSHClient = func(*ssh.Client) error { return nil }

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

		store, err := newFromDiskConfig(context.Background(), storagecore.ResolvedConfig{
			SFTPHost:                  "good",
			SFTPPassword:              "secret",
			SFTPInsecureIgnoreHostKey: true,
			Prefix:                    "pre",
		})
		if err != nil || store == nil {
			t.Fatalf("newFromDiskConfig success err=%v store=%v", err, store)
		}

		if _, err := newFromDiskConfig(context.Background(), storagecore.ResolvedConfig{
			SFTPHost:                  "bad",
			SFTPPassword:              "secret",
			SFTPInsecureIgnoreHostKey: true,
		}); err == nil {
			t.Fatal("newFromDiskConfig dial returned nil error")
		}

		newSFTPClient = func(*ssh.Client) (sftpClient, error) { return nil, errors.New("client boom") }
		if _, err := newFromDiskConfig(context.Background(), storagecore.ResolvedConfig{
			SFTPHost:                  "good",
			SFTPPassword:              "secret",
			SFTPInsecureIgnoreHostKey: true,
		}); err == nil {
			t.Fatal("newFromDiskConfig client returned nil error")
		}

		newSFTPClient = func(*ssh.Client) (sftpClient, error) { return &fakeSFTPClient{}, nil }
		if _, err := newFromDiskConfig(context.Background(), storagecore.ResolvedConfig{
			SFTPHost:                  "good",
			SFTPPassword:              "secret",
			SFTPInsecureIgnoreHostKey: true,
			Prefix:                    "../bad",
		}); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("newFromDiskConfig invalid prefix error = %v", err)
		}
	})
}

// TestSFTPPrefixHelpers verifies normalized prefix joining and exact-component stripping.
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

// TestSFTPWrapError verifies filesystem absence and permission errors retain storage identities.
func TestSFTPWrapError(t *testing.T) {
	if err := wrapError(os.ErrNotExist); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound")
	}
	if err := wrapError(os.ErrPermission); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("expected ErrForbidden")
	}
	if err := wrapError(errors.New("other")); errors.Is(err, storagecore.ErrNotFound) || errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("wrapError should preserve unrelated errors")
	}
}

// TestSFTPContextCancellation verifies canceled calls stop before accessing the SFTP client.
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
	if err := d.WalkContext(ctx, "", func(storagecore.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext error = %v", err)
	}
	if err := d.CopyContext(ctx, "file.txt", "copy.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext error = %v", err)
	}
	if err := d.MoveContext(ctx, "file.txt", "moved.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext error = %v", err)
	}
	if _, err := d.URL("file.txt"); !errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("URL error = %v", err)
	}
}

// TestSFTPResolvedConfigAndHelpers verifies configuration mapping and traversal rejection.
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
	if _, err := d.fullPath("../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("fullPath invalid error = %v", err)
	}
}

type fakeSFTPClient struct {
	openData      string
	openReader    io.ReadCloser
	openErr       error
	openFile      *fakeWriteCloser
	openFileErr   error
	openFilePath  string
	openFileFlags int
	mkdirErr      error
	removeErr     error
	removePaths   []string
	removeDirErr  error
	removeDirs    []string
	renameErr     error
	renameOld     string
	renameNew     string
	statInfo      os.FileInfo
	statErr       error
	readDir       []os.FileInfo
	readDirErr    error
}

// Open returns the configured download body, payload, or open failure.
func (f *fakeSFTPClient) Open(string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.openReader != nil {
		return f.openReader, nil
	}
	return io.NopCloser(bytes.NewBufferString(f.openData)), nil
}

// OpenFile records the temporary path and flags before returning a fake writer.
func (f *fakeSFTPClient) OpenFile(path string, flags int) (io.WriteCloser, error) {
	f.openFilePath = path
	f.openFileFlags = flags
	if f.openFileErr != nil {
		return nil, f.openFileErr
	}
	if f.openFile == nil {
		f.openFile = &fakeWriteCloser{}
	}
	return f.openFile, nil
}

// MkdirAll returns the configured parent-creation failure.
func (f *fakeSFTPClient) MkdirAll(string) error { return f.mkdirErr }

// Remove records file and temporary cleanup paths before returning its configured failure.
func (f *fakeSFTPClient) Remove(path string) error {
	f.removePaths = append(f.removePaths, path)
	return f.removeErr
}

// RemoveDirectory supports non-recursive directory deletion in the test fixture.
func (f *fakeSFTPClient) RemoveDirectory(path string) error {
	f.removeDirs = append(f.removeDirs, path)
	return f.removeDirErr
}

// PosixRename records atomic replacement endpoints and returns its configured failure.
func (f *fakeSFTPClient) PosixRename(oldname, newname string) error {
	f.renameOld = oldname
	f.renameNew = newname
	return f.renameErr
}

// Stat returns configured remote metadata or lookup failure.
func (f *fakeSFTPClient) Stat(string) (os.FileInfo, error) { return f.statInfo, f.statErr }

// ReadDir returns configured immediate children or listing failure.
func (f *fakeSFTPClient) ReadDir(string) ([]os.FileInfo, error) { return f.readDir, f.readDirErr }

// Close makes fake SFTP client cleanup succeed.
func (f *fakeSFTPClient) Close() error { return nil }

type fakeWriteCloser struct {
	buf        bytes.Buffer
	writeErr   error
	closeErr   error
	afterWrite func()
}

// Write captures bytes and can cancel or fail an upload after the write boundary.
func (f *fakeWriteCloser) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	n, err := f.buf.Write(p)
	if f.afterWrite != nil {
		f.afterWrite()
	}
	return n, err
}

// Close returns the configured temporary-writer cleanup failure.
func (f *fakeWriteCloser) Close() error { return f.closeErr }

type fakeFileInfo struct {
	name  string
	size  int64
	isDir bool
}

// Name returns the configured remote basename.
func (f fakeFileInfo) Name() string { return f.name }

// Size returns the configured remote byte length.
func (f fakeFileInfo) Size() int64 { return f.size }

// Mode marks configured fixture directories with os.ModeDir.
func (f fakeFileInfo) Mode() os.FileMode {
	if f.isDir {
		return os.ModeDir
	}
	return 0
}

// ModTime supplies a valid timestamp for os.FileInfo.
func (f fakeFileInfo) ModTime() time.Time { return time.Now() }

// IsDir returns the configured file-versus-directory distinction.
func (f fakeFileInfo) IsDir() bool { return f.isDir }

// Sys reports no platform-specific fixture metadata.
func (f fakeFileInfo) Sys() interface{} { return nil }

// TestSFTPFakeBackedOperations verifies core storage operations through the SFTP adapter boundary.
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
	if client.openFilePath == "pre/folder/file.txt" || client.renameNew != "pre/folder/file.txt" {
		t.Fatalf("Put temporary=%q rename=%q->%q", client.openFilePath, client.renameOld, client.renameNew)
	}
	if client.openFileFlags&os.O_EXCL == 0 || client.openFileFlags&os.O_TRUNC != 0 {
		t.Fatalf("Put flags = %d", client.openFileFlags)
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

// TestSFTPPutFailurePreservesDestination verifies failed or canceled uploads never replace live data.
func TestSFTPPutFailurePreservesDestination(t *testing.T) {
	t.Run("write failure removes temporary without rename", func(t *testing.T) {
		writeErr := errors.New("write boom")
		client := &fakeSFTPClient{openFile: &fakeWriteCloser{writeErr: writeErr}}
		d := &driver{client: client, prefix: "pre"}
		if err := d.Put("file.txt", []byte("replacement")); !errors.Is(err, writeErr) {
			t.Fatalf("Put error = %v", err)
		}
		if client.renameNew != "" {
			t.Fatalf("Put renamed failed upload to %q", client.renameNew)
		}
		if len(client.removePaths) != 1 || client.removePaths[0] != client.openFilePath {
			t.Fatalf("removed paths = %v temporary=%q", client.removePaths, client.openFilePath)
		}
	})

	t.Run("rename failure cleans complete temporary", func(t *testing.T) {
		renameErr := errors.New("rename boom")
		client := &fakeSFTPClient{renameErr: renameErr}
		d := &driver{client: client, prefix: "pre"}
		if err := d.Put("file.txt", []byte("replacement")); !errors.Is(err, renameErr) {
			t.Fatalf("Put error = %v", err)
		}
		if client.renameNew != "pre/file.txt" || len(client.removePaths) != 1 || client.removePaths[0] != client.openFilePath {
			t.Fatalf("rename=%q->%q removed=%v temporary=%q", client.renameOld, client.renameNew, client.removePaths, client.openFilePath)
		}
	})

	t.Run("cancellation after write aborts before rename", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		client := &fakeSFTPClient{openFile: &fakeWriteCloser{afterWrite: cancel}}
		d := &driver{client: client, prefix: "pre"}
		if err := d.PutContext(ctx, "file.txt", []byte("replacement")); !errors.Is(err, context.Canceled) {
			t.Fatalf("PutContext error = %v", err)
		}
		if client.renameNew != "" || len(client.removePaths) != 1 {
			t.Fatalf("rename target=%q removed=%v", client.renameNew, client.removePaths)
		}
	})
}

// TestSFTPRootAndSamePathMutations verifies root guards and source-validating no-op operations.
func TestSFTPRootAndSamePathMutations(t *testing.T) {
	for _, prefix := range []string{"", "pre"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			client := &fakeSFTPClient{
				openData: "payload",
				statInfo: fakeFileInfo{name: "file.txt", size: 7},
			}
			d := &driver{client: client, prefix: prefix}
			if err := d.Copy("file.txt", "file.txt"); err != nil {
				t.Fatalf("Copy same path: %v", err)
			}
			if err := d.Move("file.txt", "file.txt"); err != nil {
				t.Fatalf("Move same path: %v", err)
			}
			if client.renameNew != "" {
				t.Fatalf("same-path operations renamed to %q", client.renameNew)
			}
			client.statErr = os.ErrNotExist
			if err := d.Move("missing", "missing"); !errors.Is(err, storagecore.ErrNotFound) {
				t.Fatalf("Move missing same path error = %v", err)
			}
			client.statErr = nil
			for name, err := range map[string]error{
				"put":         d.Put("", []byte("root")),
				"copy source": d.Copy("", "file.txt"),
				"copy target": d.Copy("file.txt", ""),
				"move source": d.Move("", "other"),
				"move target": d.Move("file.txt", ""),
				"delete":      d.Delete(""),
			} {
				if !errors.Is(err, storagecore.ErrForbidden) {
					t.Errorf("%s root error = %v", name, err)
				}
			}
		})
	}
}

// TestSFTPDeleteDispatchesByType verifies directories use the server's non-recursive operation.
func TestSFTPDeleteDispatchesByType(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		client := &fakeSFTPClient{statInfo: fakeFileInfo{name: "file.txt"}}
		d := &driver{client: client}
		if err := d.Delete("file.txt"); err != nil {
			t.Fatalf("Delete file: %v", err)
		}
		if len(client.removePaths) != 1 || len(client.removeDirs) != 0 {
			t.Fatalf("file removal calls: files=%v dirs=%v", client.removePaths, client.removeDirs)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		client := &fakeSFTPClient{statInfo: fakeFileInfo{name: "folder", isDir: true}}
		d := &driver{client: client}
		if err := d.Delete("folder"); err != nil {
			t.Fatalf("Delete directory: %v", err)
		}
		if len(client.removeDirs) != 1 || len(client.removePaths) != 0 {
			t.Fatalf("directory removal calls: files=%v dirs=%v", client.removePaths, client.removeDirs)
		}
	})

	t.Run("nonempty directory", func(t *testing.T) {
		client := &fakeSFTPClient{
			statInfo:     fakeFileInfo{name: "folder", isDir: true},
			removeDirErr: syscall.ENOTEMPTY,
		}
		d := &driver{client: client}
		if err := d.Delete("folder"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Delete nonempty directory error = %v", err)
		}
	})
}

// TestSFTPLogicalRootIsSynthetic verifies prefixes never expose an exact backend object.
func TestSFTPLogicalRootIsSynthetic(t *testing.T) {
	tests := []struct {
		name   string
		client *fakeSFTPClient
		prefix string
	}{
		{name: "unprefixed empty root", client: &fakeSFTPClient{}, prefix: ""},
		{name: "missing prefix", client: &fakeSFTPClient{statErr: os.ErrNotExist, readDirErr: os.ErrNotExist}, prefix: "pre"},
		{name: "exact prefix object", client: &fakeSFTPClient{statInfo: fakeFileInfo{name: "pre"}, readDirErr: errors.New("not a directory")}, prefix: "pre"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &driver{client: tt.client, prefix: tt.prefix}
			if _, err := d.Get(""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("Get root error = %v", err)
			}
			entry, err := d.Stat("")
			if err != nil || entry.Path != "" || !entry.IsDir {
				t.Fatalf("Stat root = %+v err=%v", entry, err)
			}
			if exists, err := d.Exists(""); err != nil || exists {
				t.Fatalf("Exists root = %v err=%v", exists, err)
			}
			if entries, err := d.List(""); err != nil || len(entries) != 0 {
				t.Fatalf("List root = %+v err=%v", entries, err)
			}
			called := false
			if err := d.Walk("", func(storagecore.Entry) error { called = true; return nil }); err != nil || called {
				t.Fatalf("Walk root called=%v err=%v", called, err)
			}
			if err := d.Copy("", "copy.txt"); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("Copy source root error = %v", err)
			}
		})
	}
}

// TestSFTPFakeWalkAndErrors verifies recursive callbacks, error mapping, and object-only existence.
func TestSFTPFakeWalkAndErrors(t *testing.T) {
	t.Run("walk file path", func(t *testing.T) {
		d := &driver{
			client: &fakeSFTPClient{statInfo: fakeFileInfo{name: "file.txt", size: 4}},
			prefix: "pre",
		}
		var got []storagecore.Entry
		if err := d.Walk("file.txt", func(entry storagecore.Entry) error {
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
		err := d.Walk("folder", func(entry storagecore.Entry) error {
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
		if _, err := d.Get("file.txt"); !errors.Is(err, storagecore.ErrNotFound) {
			t.Fatalf("Get error = %v", err)
		}
		if err := d.Put("file.txt", []byte("x")); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Put error = %v", err)
		}
		if err := d.Delete("file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Delete error = %v", err)
		}
		if _, err := d.Stat("file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Stat error = %v", err)
		}
		if _, err := d.Exists("file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Exists error = %v", err)
		}
		if _, err := d.List("file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
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

// Read injects a deterministic failure after a download opens.
func (failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// Close makes cleanup of the failing test reader succeed.
func (failingReadCloser) Close() error { return nil }
