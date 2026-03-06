package localstorage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goforj/storage"
)

func TestLocalConstructors(t *testing.T) {
	t.Run("new missing remote", func(t *testing.T) {
		_, err := New(Config{})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("new context success", func(t *testing.T) {
		root := t.TempDir()
		got, err := NewContext(context.Background(), Config{Remote: root, Prefix: "pre"})
		if err != nil {
			t.Fatalf("NewContext: %v", err)
		}
		if got == nil {
			t.Fatal("NewContext returned nil storage")
		}
	})
}

func TestWrapLocalError(t *testing.T) {
	if err := wrapLocalError(os.ErrNotExist); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := wrapLocalError(os.ErrPermission); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if err := wrapLocalError(errors.New("other")); !errors.Is(err, storage.ErrForbidden) && !errors.Is(err, storage.ErrNotFound) {
		// pass; should be unchanged
	} else {
		t.Fatalf("expected passthrough error")
	}
}

func TestResolvePathAndTraversal(t *testing.T) {
	d := &driver{root: "/tmp/root", prefix: "pre"}
	// valid path
	got, err := d.resolvePath("file.txt")
	if err != nil {
		t.Fatalf("ResolvePath error: %v", err)
	}
	if got == "" {
		t.Fatalf("expected path, got empty")
	}
	// traversal rejection
	if _, err := d.resolvePath("../etc/passwd"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for traversal, got %v", err)
	}
}

func TestLocalURLUnsupported(t *testing.T) {
	d := &driver{}
	if _, err := d.URL("x"); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestLocalModTimeAndUserRelative(t *testing.T) {
	root := t.TempDir()
	d := &driver{root: root, prefix: "pre"}

	target := filepath.Join(root, "pre", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mt, err := d.modTime(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("modTime: %v", err)
	}
	if mt.IsZero() || mt.After(time.Now().UTC().Add(2*time.Second)) {
		t.Fatalf("unexpected modTime %v", mt)
	}

	rel, err := d.userRelative(target)
	if err != nil {
		t.Fatalf("userRelative: %v", err)
	}
	if rel != "file.txt" {
		t.Fatalf("userRelative = %q", rel)
	}
}

func TestLocalContextCancellation(t *testing.T) {
	root := t.TempDir()
	d := &driver{root: root, prefix: "pre"}

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

func TestLocalWalkFileAndCallbackError(t *testing.T) {
	root := t.TempDir()
	d := &driver{root: root, prefix: "pre"}

	target := filepath.Join(root, "pre", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var got storage.Entry
	if err := d.WalkContext(context.Background(), "file.txt", func(entry storage.Entry) error {
		got = entry
		return nil
	}); err != nil {
		t.Fatalf("WalkContext file: %v", err)
	}
	if got.Path != "file.txt" || got.IsDir {
		t.Fatalf("WalkContext file entry = %+v", got)
	}

	want := errors.New("stop")
	err := d.WalkContext(context.Background(), "", func(storage.Entry) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("WalkContext callback error = %v", err)
	}
}
