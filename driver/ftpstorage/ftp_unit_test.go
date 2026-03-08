package ftpstorage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/textproto"
	"syscall"
	"testing"

	"github.com/goforj/storage"
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
	if _, err := d.fullPath("../bad"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("fullPath invalid error = %v", err)
	}
}

func TestFTPWrapError(t *testing.T) {
	if err := wrapError(errors.New("file not found")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := wrapError(errors.New("File not available")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for case-insensitive match")
	}
	if err := wrapError(nil); err != nil {
		t.Fatalf("wrapError(nil) = %v", err)
	}
	if err := wrapError(errors.New("boom")); errors.Is(err, storage.ErrNotFound) {
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
		{name: "not found", err: storage.ErrNotFound, want: false},
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
