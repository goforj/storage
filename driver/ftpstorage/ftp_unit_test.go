package ftpstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/textproto"
	"syscall"
	"testing"

	"github.com/goforj/storage/storagecore"
	"github.com/jlaffaye/ftp"
)

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
}

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

func TestFTPWrapError(t *testing.T) {
	if err := wrapError(errors.New("file not found")); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := wrapError(errors.New("File not available")); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for case-insensitive match")
	}
	if err := wrapError(nil); err != nil {
		t.Fatalf("wrapError(nil) = %v", err)
	}
	if err := wrapError(errors.New("boom")); errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("wrapError should preserve unrelated errors")
	}
}

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
	loginErr    error
	quitErr     error
	retrData    string
	retrReader  io.ReadCloser
	retrErr     error
	stored      bytes.Buffer
	storErr     error
	deleteErr   error
	listEntries []*ftp.Entry
	listErr     error
	fileSize    int64
	fileSizeErr error
	makeDirErr  error
}

func (f *fakeFTPConn) Login(string, string) error        { return f.loginErr }
func (f *fakeFTPConn) Quit() error                       { return f.quitErr }
func (f *fakeFTPConn) Delete(string) error               { return f.deleteErr }
func (f *fakeFTPConn) FileSize(string) (int64, error)    { return f.fileSize, f.fileSizeErr }
func (f *fakeFTPConn) MakeDir(string) error              { return f.makeDirErr }
func (f *fakeFTPConn) Rename(string, string) error       { return f.storErr }
func (f *fakeFTPConn) List(string) ([]*ftp.Entry, error) { return f.listEntries, f.listErr }
func (f *fakeFTPConn) Retr(string) (io.ReadCloser, error) {
	if f.retrErr != nil {
		return nil, f.retrErr
	}
	if f.retrReader != nil {
		return f.retrReader, nil
	}
	return io.NopCloser(bytes.NewBufferString(f.retrData)), nil
}
func (f *fakeFTPConn) Stor(_ string, r io.Reader) error {
	if f.storErr != nil {
		return f.storErr
	}
	_, err := io.Copy(&f.stored, r)
	return err
}

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
			listEntries: []*ftp.Entry{
				{Name: "sub", Type: ftp.EntryTypeFolder},
				{Name: "file.txt", Size: 1, Type: ftp.EntryTypeFile},
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
