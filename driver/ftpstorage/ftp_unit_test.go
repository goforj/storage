package ftpstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/textproto"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/goforj/storage/storagecore"
	"github.com/jlaffaye/ftp"
)

// TestFTPConstructors verifies registry identity, required host validation, and TLS option constraints.
func TestFTPConstructors(t *testing.T) {
	if got := (Config{}).DriverName(); got != "ftp" {
		t.Fatalf("DriverName = %q", got)
	}

	t.Run("new missing host", func(t *testing.T) {
		_, err := New(Config{})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("new context applies defaults", func(t *testing.T) {
		got, err := NewContext(context.Background(), Config{Host: "127.0.0.1", Prefix: "pre"})
		if err != nil {
			t.Fatalf("NewContext: %v", err)
		}
		if got == nil {
			t.Fatal("NewContext returned nil storage")
		}
	})

	t.Run("TLS options require TLS", func(t *testing.T) {
		_, err := NewContext(context.Background(), Config{Host: "127.0.0.1", InsecureSkipVerify: true})
		if err == nil {
			t.Fatal("NewContext accepted a TLS-only option without TLS")
		}
	})
}

// TestFTPPrefixHelpers verifies exact-component stripping and traversal rejection.
func TestFTPPrefixHelpers(t *testing.T) {
	d := &driver{prefix: "pre"}
	if got := d.stripPrefix("pre/path/to"); got != "path/to" {
		t.Fatalf("stripPrefix got %q", got)
	}
	if got := (&driver{}).stripPrefix("plain/path"); got != "plain/path" {
		t.Fatalf("stripPrefix without prefix got %q", got)
	}
	if _, err := d.fullPath("../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("fullPath invalid error = %v", err)
	}
}

// TestFTPWrapError verifies FTP text replies retain portable absence and permission identities.
func TestFTPWrapError(t *testing.T) {
	if err := wrapError(errors.New("file not found")); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := wrapError(errors.New("File not available")); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for case-insensitive match")
	}
	if err := wrapError(&textproto.Error{Code: 550, Msg: "Permission denied"}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("expected ErrForbidden before generic 550 classification, got %v", err)
	}
	if err := wrapError(&textproto.Error{Code: 550, Msg: "Directory not empty"}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-empty directory, got %v", err)
	}
	if err := wrapError(nil); err != nil {
		t.Fatalf("wrapError(nil) = %v", err)
	}
	if err := wrapError(errors.New("boom")); errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("wrapError should preserve unrelated errors")
	}
}

// TestFTPContextCancellation verifies canceled operations stop before touching a connection.
func TestFTPContextCancellation(t *testing.T) {
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
	if err := d.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
}

// TestShouldReconnectFTP verifies retries are limited to transient control and transport failures.
func TestShouldReconnectFTP(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "not found", err: storagecore.ErrNotFound, want: false},
		{name: "context canceled", err: context.Canceled, want: false},
		{name: "eof", err: io.EOF, want: true},
		{name: "net closed", err: net.ErrClosed, want: true},
		{name: "broken pipe", err: syscall.EPIPE, want: true},
		{name: "ftp 421", err: &textproto.Error{Code: 421, Msg: "service not available"}, want: true},
		{name: "ftp 550", err: &textproto.Error{Code: 550, Msg: "not found"}, want: false},
		{name: "closed network", err: errors.New("use of closed network connection"), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReconnectFTP(tt.err); got != tt.want {
				t.Fatalf("shouldReconnectFTP(%v) = %v want %v", tt.err, got, tt.want)
			}
		})
	}
}

type fakeFTPConn struct {
	loginErr       error
	quitErr        error
	retrData       string
	retrReader     io.ReadCloser
	retrErr        error
	stored         bytes.Buffer
	storErr        error
	deleteErr      error
	removeDirErr   error
	removeDirCalls int
	listEntries    []*ftp.Entry
	listErr        error
	listFn         func(string) ([]*ftp.Entry, error)
	fileSize       int64
	fileSizeErr    error
	makeDirErr     error
}

// TestFTPInvalidPathsAndThinWrappers covers validation before any connection
// request and the background-context adapters omitted by the shared contract.
func TestFTPInvalidPathsAndThinWrappers(t *testing.T) {
	d := &driver{}
	calls := []func() error{
		func() error { _, err := d.GetContext(nil, "../bad"); return err },
		func() error { return d.PutContext(nil, "../bad", nil) },
		func() error { return d.MakeDirContext(nil, "../bad") },
		func() error { return d.DeleteContext(nil, "../bad") },
		func() error { _, err := d.StatContext(nil, "../bad"); return err },
		func() error { _, err := d.ExistsContext(nil, "../bad"); return err },
		func() error { _, err := d.ListContext(nil, "../bad"); return err },
		func() error { return d.WalkContext(nil, "../bad", func(storagecore.Entry) error { return nil }) },
		func() error { return d.CopyContext(nil, "../bad", "dst") },
		func() error { return d.CopyContext(nil, "src", "../bad") },
		func() error { return d.MoveContext(nil, "../bad", "dst") },
		func() error { return d.MoveContext(nil, "src", "../bad") },
	}
	for index, call := range calls {
		if err := call(); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("invalid-path call %d error = %v", index, err)
		}
	}
	if err := d.MakeDir("../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("MakeDir invalid path error = %v", err)
	}
	if _, err := d.ListPage("../bad", 0, 1); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ListPage error = %v", err)
	}
}

// Login returns the configured authentication failure.
func (f *fakeFTPConn) Login(string, string) error { return f.loginErr }

// Quit returns the configured connection-cleanup failure.
func (f *fakeFTPConn) Quit() error { return f.quitErr }

// Delete returns the configured file-removal failure.
func (f *fakeFTPConn) Delete(string) error { return f.deleteErr }

// RemoveDir records non-recursive directory removal and returns its configured failure.
func (f *fakeFTPConn) RemoveDir(string) error {
	f.removeDirCalls++
	return f.removeDirErr
}

// FileSize returns configured object metadata for existence and walk probes.
func (f *fakeFTPConn) FileSize(string) (int64, error) { return f.fileSize, f.fileSizeErr }

// MakeDir returns the configured recursive-parent creation result.
func (f *fakeFTPConn) MakeDir(string) error { return f.makeDirErr }

// Rename reuses the configured mutation failure for move tests.
func (f *fakeFTPConn) Rename(string, string) error { return f.storErr }

// List returns configured entries or delegates path-sensitive fixture behavior.
func (f *fakeFTPConn) List(path string) ([]*ftp.Entry, error) {
	if f.listFn != nil {
		return f.listFn(path)
	}
	return f.listEntries, f.listErr
}

// Retr opens the configured download stream or payload.
func (f *fakeFTPConn) Retr(string) (io.ReadCloser, error) {
	if f.retrErr != nil {
		return nil, f.retrErr
	}
	if f.retrReader != nil {
		return f.retrReader, nil
	}
	return io.NopCloser(bytes.NewBufferString(f.retrData)), nil
}

// Stor captures an upload unless a configured failure interrupts it.
func (f *fakeFTPConn) Stor(_ string, r io.Reader) error {
	if f.storErr != nil {
		return f.storErr
	}
	_, err := io.Copy(&f.stored, r)
	return err
}

// TestFTPFakeBackedOperations verifies core object operations against the FTP adapter boundary.
func TestFTPFakeBackedOperations(t *testing.T) {
	conn := &fakeFTPConn{
		retrData: "hello",
		fileSize: 5,
		listEntries: []*ftp.Entry{
			{Name: "file.txt", Size: 5, Type: ftp.EntryTypeFile},
			{Name: "dir", Type: ftp.EntryTypeFolder},
		},
	}
	d := &driver{
		prefix: "pre",
		conn:   conn,
		dialFn: func() (ftpConn, error) { return conn, nil },
	}

	data, err := d.Get("file.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("Get = %q err=%v", data, err)
	}
	if err := d.Put("dir/file.txt", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := conn.stored.String(); got != "payload" {
		t.Fatalf("stored payload = %q", got)
	}
	if err := d.Delete("file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entry, err := d.Stat("file.txt")
	if err != nil || entry.Path != "file.txt" || entry.Size != 5 {
		t.Fatalf("Stat = %+v err=%v", entry, err)
	}
	ok, err := d.Exists("file.txt")
	if err != nil || !ok {
		t.Fatalf("Exists = %v err=%v", ok, err)
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List = %+v err=%v", entries, err)
	}
}

// TestFTPDirectoryDeleteAndRootGuards verifies directory deletion is non-recursive and root mutations fail.
func TestFTPDirectoryDeleteAndRootGuards(t *testing.T) {
	conn := &fakeFTPConn{
		listEntries: []*ftp.Entry{{Name: "folder", Type: ftp.EntryTypeFolder}},
	}
	d := &driver{prefix: "pre", conn: conn, dialFn: func() (ftpConn, error) { return conn, nil }}
	if err := d.Delete("folder"); err != nil {
		t.Fatalf("Delete directory: %v", err)
	}
	if conn.removeDirCalls != 1 {
		t.Fatalf("RemoveDir calls = %d want 1", conn.removeDirCalls)
	}
	for name, err := range map[string]error{
		"put":         d.Put("", []byte("root")),
		"get":         func() error { _, err := d.Get(""); return err }(),
		"copy source": d.Copy("", "folder"),
		"copy target": d.Copy("folder", ""),
		"move source": d.Move("", "other"),
		"move target": d.Move("folder", ""),
		"delete":      d.Delete(""),
	} {
		if !errors.Is(err, storagecore.ErrForbidden) {
			t.Errorf("%s root error = %v", name, err)
		}
	}
}

// TestFTPLogicalRootIsSynthetic verifies missing prefixes and exact backend objects remain outside the namespace.
func TestFTPLogicalRootIsSynthetic(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		conn   *fakeFTPConn
	}{
		{name: "unprefixed root", conn: &fakeFTPConn{}},
		{name: "missing or exact prefix", prefix: "pre", conn: &fakeFTPConn{
			listErr:     &textproto.Error{Code: 550, Msg: "not found"},
			fileSize:    7,
			fileSizeErr: nil,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &driver{prefix: tt.prefix, conn: tt.conn, dialFn: func() (ftpConn, error) { return tt.conn, nil }}
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
			if _, err := d.Get(""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("Get root error = %v", err)
			}
			if err := d.Copy("", "copy.txt"); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("Copy source root error = %v", err)
			}
		})
	}
}

// TestFTPSamePathMoveValidatesSource verifies no-op moves still require an existing source.
func TestFTPSamePathMoveValidatesSource(t *testing.T) {
	for _, prefix := range []string{"", "pre"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			conn := &fakeFTPConn{
				listEntries: []*ftp.Entry{{Name: "file.txt", Size: 7, Type: ftp.EntryTypeFile}},
			}
			d := &driver{prefix: prefix, conn: conn, dialFn: func() (ftpConn, error) { return conn, nil }}
			if err := d.Move("file.txt", "file.txt"); err != nil {
				t.Fatalf("Move same path: %v", err)
			}
			conn.listEntries = nil
			if err := d.Move("missing", "missing"); !errors.Is(err, storagecore.ErrNotFound) {
				t.Fatalf("Move missing same path error = %v", err)
			}
		})
	}
}

// TestFTPFakeWalkAndErrors verifies file fallback, recursive ordering, callbacks, and adapter failures.
func TestFTPFakeWalkAndErrors(t *testing.T) {
	t.Run("walk file path fallback", func(t *testing.T) {
		conn := &fakeFTPConn{
			listErr:     &textproto.Error{Code: 550, Msg: "not found"},
			fileSize:    4,
			fileSizeErr: nil,
		}
		d := &driver{prefix: "pre", dialFn: func() (ftpConn, error) { return conn, nil }}
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

	t.Run("walk recursive callback error", func(t *testing.T) {
		conn := &fakeFTPConn{
			listFn: func(path string) ([]*ftp.Entry, error) {
				if path == "pre/sub" {
					return nil, nil
				}
				return []*ftp.Entry{
					{Name: "sub", Type: ftp.EntryTypeFolder},
					{Name: "file.txt", Size: 1, Type: ftp.EntryTypeFile},
				}, nil
			},
		}
		d := &driver{prefix: "pre", dialFn: func() (ftpConn, error) { return conn, nil }}
		stop := errors.New("stop")
		err := d.Walk("", func(entry storagecore.Entry) error {
			if entry.Path == "sub" {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) {
			t.Fatalf("Walk callback error = %v", err)
		}
	})

	t.Run("walk callback can re-enter driver", func(t *testing.T) {
		conn := &fakeFTPConn{
			retrData: "payload",
			listEntries: []*ftp.Entry{
				{Name: "file.txt", Size: 7, Type: ftp.EntryTypeFile},
			},
		}
		d := &driver{prefix: "pre", conn: conn, dialFn: func() (ftpConn, error) { return conn, nil }}
		done := make(chan error, 1)
		go func() {
			done <- d.Walk("", func(entry storagecore.Entry) error {
				data, err := d.Get(entry.Path)
				if err != nil {
					return err
				}
				if string(data) != "payload" {
					return errors.New("unexpected re-entrant payload")
				}
				return nil
			})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Walk re-entry: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Walk callback deadlocked while re-entering the driver")
		}
	})

	t.Run("walk retry does not duplicate callbacks", func(t *testing.T) {
		first := &fakeFTPConn{
			listFn: func(path string) ([]*ftp.Entry, error) {
				if path == "pre/tree" {
					return nil, io.EOF
				}
				return []*ftp.Entry{{Name: "tree", Type: ftp.EntryTypeFolder}}, nil
			},
		}
		second := &fakeFTPConn{
			listFn: func(path string) ([]*ftp.Entry, error) {
				if path == "pre/tree" {
					return []*ftp.Entry{{Name: "leaf.txt", Size: 1, Type: ftp.EntryTypeFile}}, nil
				}
				return []*ftp.Entry{{Name: "tree", Type: ftp.EntryTypeFolder}}, nil
			},
		}
		calls := 0
		d := &driver{
			prefix: "pre",
			dialFn: func() (ftpConn, error) {
				calls++
				if calls == 1 {
					return first, nil
				}
				return second, nil
			},
		}
		var got []string
		if err := d.Walk("", func(entry storagecore.Entry) error {
			got = append(got, entry.Path)
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		want := []string{"tree", "tree/leaf.txt"}
		if !slices.Equal(got, want) {
			t.Fatalf("Walk callbacks = %v want %v", got, want)
		}
	})

	t.Run("operation errors", func(t *testing.T) {
		conn := &fakeFTPConn{
			retrErr:     errors.New("550 missing"),
			storErr:     errors.New("stor boom"),
			deleteErr:   errors.New("delete boom"),
			listErr:     errors.New("list boom"),
			fileSizeErr: errors.New("size boom"),
		}
		d := &driver{prefix: "pre", dialFn: func() (ftpConn, error) { return conn, nil }}
		if _, err := d.Get("file.txt"); !errors.Is(err, storagecore.ErrNotFound) {
			t.Fatalf("Get error = %v", err)
		}
		if err := d.Put("file.txt", []byte("x")); err == nil {
			t.Fatal("Put returned nil error")
		}
		if err := d.Delete("file.txt"); err == nil {
			t.Fatal("Delete returned nil error")
		}
		if _, err := d.Stat("file.txt"); err == nil {
			t.Fatal("Stat returned nil error")
		}
		if _, err := d.Exists("file.txt"); err == nil {
			t.Fatal("Exists returned nil error")
		}
		if _, err := d.List("file.txt"); err == nil {
			t.Fatal("List returned nil error")
		}
	})

	t.Run("read failure and reconnect", func(t *testing.T) {
		first := &fakeFTPConn{retrErr: io.EOF}
		second := &fakeFTPConn{retrData: "recovered"}
		calls := 0
		d := &driver{
			prefix: "pre",
			dialFn: func() (ftpConn, error) {
				calls++
				if calls == 1 {
					return first, nil
				}
				return second, nil
			},
		}
		data, err := d.Get("file.txt")
		if err != nil || string(data) != "recovered" {
			t.Fatalf("Get recovered = %q err=%v", data, err)
		}
	})
}

// TestFTPLifecycleAndMutationEdges covers connection setup, pagination,
// relocation failures, callback validation, and terminal close behavior.
func TestFTPLifecycleAndMutationEdges(t *testing.T) {
	t.Run("dial and login failures", func(t *testing.T) {
		dialErr := errors.New("dial")
		d := &driver{dialFn: func() (ftpConn, error) { return nil, dialErr }}
		if _, err := d.Get("file"); !errors.Is(err, dialErr) {
			t.Fatalf("Get dial error = %v", err)
		}
		loginErr := errors.New("login")
		d = &driver{user: "user", dialFn: func() (ftpConn, error) { return &fakeFTPConn{loginErr: loginErr}, nil }}
		if _, err := d.Get("file"); !errors.Is(err, loginErr) {
			t.Fatalf("Get login error = %v", err)
		}
	})

	t.Run("pagination callback and mkdir", func(t *testing.T) {
		conn := &fakeFTPConn{listEntries: []*ftp.Entry{
			{Name: "a", Type: ftp.EntryTypeFile},
			{Name: "b", Type: ftp.EntryTypeFile},
		}}
		d := &driver{conn: conn, dialFn: func() (ftpConn, error) { return conn, nil }}
		page, err := d.ListPage("", 0, 1)
		if err != nil || len(page.Entries) != 1 || !page.HasMore {
			t.Fatalf("ListPage = %+v, %v", page, err)
		}
		if err := d.Walk("", nil); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("Walk nil callback error = %v", err)
		}
		if err := d.MakeDir(""); err != nil {
			t.Fatalf("MakeDir root: %v", err)
		}
		conn.makeDirErr = errors.New("mkdir")
		if err := d.MakeDir("dir"); err == nil {
			t.Fatal("MakeDir returned nil error")
		}
	})

	t.Run("copy and move failures", func(t *testing.T) {
		conn := &fakeFTPConn{retrErr: errors.New("550 missing"), fileSizeErr: errors.New("550 missing")}
		d := &driver{conn: conn, dialFn: func() (ftpConn, error) { return conn, nil }}
		if err := d.Copy("missing", "copy"); !errors.Is(err, storagecore.ErrNotFound) {
			t.Fatalf("Copy missing error = %v", err)
		}
		if err := d.Move("missing", "moved"); !errors.Is(err, storagecore.ErrNotFound) {
			t.Fatalf("Move missing error = %v", err)
		}
		conn = &fakeFTPConn{
			listEntries: []*ftp.Entry{{Name: "file", Type: ftp.EntryTypeFile}},
			storErr:     errors.New("rename"),
		}
		d = &driver{conn: conn, dialFn: func() (ftpConn, error) { return conn, nil }}
		if err := d.Move("file", "moved"); err == nil {
			t.Fatal("Move rename returned nil error")
		}
	})

	t.Run("close is terminal", func(t *testing.T) {
		quitErr := errors.New("quit")
		conn := &fakeFTPConn{quitErr: quitErr}
		d := &driver{conn: conn, dialFn: func() (ftpConn, error) { return conn, nil }}
		if err := d.Close(); !errors.Is(err, quitErr) {
			t.Fatalf("Close error = %v", err)
		}
		if err := d.Close(); !errors.Is(err, quitErr) {
			t.Fatalf("second Close error = %v", err)
		}
		calls := []func() error{
			func() error { _, err := d.Get("file"); return err },
			func() error { return d.Put("file", nil) },
			func() error { return d.MakeDir("dir") },
			func() error { return d.Delete("file") },
			func() error { _, err := d.Stat("file"); return err },
			func() error { _, err := d.Exists("file"); return err },
			func() error { _, err := d.List(""); return err },
			func() error { return d.Walk("", func(storagecore.Entry) error { return nil }) },
			func() error { return d.Copy("file", "copy") },
			func() error { return d.Move("file", "moved") },
		}
		for index, call := range calls {
			if err := call(); err == nil {
				t.Fatalf("closed call %d returned nil error", index)
			}
		}
	})
}

// TestFTPPureHelperEdges covers configuration and stream helpers that do not
// require an FTP server.
func TestFTPPureHelperEdges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newFromDiskConfig(ctx, storagecore.ResolvedConfig{FTPHost: "host"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("newFromDiskConfig canceled error = %v", err)
	}
	for _, port := range []int{-1, 65536} {
		if _, err := New(Config{Host: "host", Port: port}); err == nil {
			t.Fatalf("New accepted port %d", port)
		}
	}
	if _, err := New(Config{Host: "host", Prefix: "../bad"}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("New invalid prefix error = %v", err)
	}
	store, err := New(Config{Host: "host", TLS: true})
	if err != nil {
		t.Fatalf("New TLS: %v", err)
	}
	if d := store.(*driver); !d.tls || d.serverName != "host" {
		t.Fatalf("TLS driver = %+v", d)
	}

	d := &driver{}
	if err := d.runConnLocked(func(ftpConn) error { return nil }); err == nil {
		t.Fatal("runConnLocked without connection returned nil error")
	}
	d.closed = true
	if _, err := d.ensureConnLocked(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("ensureConnLocked closed error = %v", err)
	}
	if isDirectoryExistsError(nil) {
		t.Fatal("isDirectoryExistsError(nil) = true")
	}
	if !shouldReconnectFTP(temporaryNetError{}) {
		t.Fatal("shouldReconnectFTP net error = false")
	}

	if _, err := (&contextReader{ctx: ctx, reader: bytes.NewReader(nil)}).Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("contextReader canceled error = %v", err)
	}
	if _, err := copyContext(ctx, io.Discard, bytes.NewReader(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("copyContext canceled error = %v", err)
	}
	writeErr := errors.New("write")
	if _, err := copyContext(context.Background(), ftpFailingWriter{err: writeErr}, bytes.NewReader([]byte("x"))); !errors.Is(err, writeErr) {
		t.Fatalf("copyContext write error = %v", err)
	}
	if _, err := copyContext(context.Background(), ftpShortWriter{}, bytes.NewReader([]byte("x"))); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("copyContext short write error = %v", err)
	}
	if _, err := copyContext(context.Background(), io.Discard, ftpErrorReader{err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("copyContext read error = %v", err)
	}
	cleanup := errors.New("cleanup")
	if err := joinCleanup(nil, cleanup); !errors.Is(err, cleanup) {
		t.Fatalf("joinCleanup cleanup error = %v", err)
	}
	if err := joinCleanup(writeErr, cleanup); !errors.Is(err, writeErr) || !errors.Is(err, cleanup) {
		t.Fatalf("joinCleanup combined error = %v", err)
	}
}

type temporaryNetError struct{}

// Error returns a stable network failure message.
func (temporaryNetError) Error() string { return "temporary network failure" }

// Timeout classifies the fixture as a non-timeout network error.
func (temporaryNetError) Timeout() bool { return false }

// Temporary classifies the fixture as a retryable network error.
func (temporaryNetError) Temporary() bool { return true }

type ftpFailingWriter struct {
	err error
}

// Write injects a destination failure.
func (w ftpFailingWriter) Write([]byte) (int, error) { return 0, w.err }

type ftpShortWriter struct{}

// Write accepts no bytes without error to exercise short-write handling.
func (ftpShortWriter) Write([]byte) (int, error) { return 0, nil }

type ftpErrorReader struct {
	err error
}

// Read injects a source failure.
func (r ftpErrorReader) Read([]byte) (int, error) { return 0, r.err }
