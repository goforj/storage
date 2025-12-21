package local

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/goforj/filesystem"
)

func TestWrapLocalError(t *testing.T) {
	if err := wrapLocalError(os.ErrNotExist); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := wrapLocalError(os.ErrPermission); !errors.Is(err, filesystem.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if err := wrapLocalError(errors.New("other")); !errors.Is(err, filesystem.ErrForbidden) && !errors.Is(err, filesystem.ErrNotFound) {
		// pass; should be unchanged
	} else {
		t.Fatalf("expected passthrough error")
	}
}

func TestResolvePathAndTraversal(t *testing.T) {
	d := &Driver{root: "/tmp/root", prefix: "pre"}
	// valid path
	got, err := d.ResolvePath("file.txt")
	if err != nil {
		t.Fatalf("ResolvePath error: %v", err)
	}
	if got == "" {
		t.Fatalf("expected path, got empty")
	}
	// traversal rejection
	if _, err := d.ResolvePath("../etc/passwd"); !errors.Is(err, filesystem.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for traversal, got %v", err)
	}
}

func TestLocalURLUnsupported(t *testing.T) {
	d := &Driver{}
	if _, err := d.URL(context.Background(), "x"); !errors.Is(err, filesystem.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
