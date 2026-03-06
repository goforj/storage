package sftpstorage

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/goforj/storage"
)

func TestSFTPConstructorsAndAuth(t *testing.T) {
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
}
