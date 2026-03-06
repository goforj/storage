package localstorage

import (
	"errors"
	"os"
	"testing"

	"github.com/goforj/storage"
)

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
