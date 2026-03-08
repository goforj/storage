package sftpstorage

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/goforj/storage"
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
