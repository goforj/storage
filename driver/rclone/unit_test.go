package rclone

import (
	"errors"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"

	"github.com/goforj/storage"
)

func TestRcloneWrapError(t *testing.T) {
	if err := wrapError(fs.ErrorObjectNotFound); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := wrapError(fs.ErrorPermissionDenied); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if err := wrapError(hash.ErrUnsupported); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestRcloneIsNotFound(t *testing.T) {
	if !isNotFound(fs.ErrorObjectNotFound) {
		t.Fatalf("expected true for object not found")
	}
	if !isNotFound(fs.ErrorDirNotFound) {
		t.Fatalf("expected true for dir not found")
	}
	if isNotFound(errors.New("other")) {
		t.Fatalf("expected false for other errors")
	}
}

func TestRclonePrefixHelpers(t *testing.T) {
	d := &Driver{prefix: "pre"}
	fp, err := d.fullPath("file.txt")
	if err != nil {
		t.Fatalf("fullPath err: %v", err)
	}
	if fp != "pre/file.txt" {
		t.Fatalf("unexpected fullPath: %q", fp)
	}
	if got := d.stripPrefix("pre/sub/file.txt"); got != "sub/file.txt" {
		t.Fatalf("stripPrefix got %q", got)
	}
}
