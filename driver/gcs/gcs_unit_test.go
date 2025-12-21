package gcsdriver

import (
	"errors"
	"testing"

	"cloud.google.com/go/storage"

	"github.com/goforj/filesystem"
)

func TestGCSKeyAndPrefixHelpers(t *testing.T) {
	d := &Driver{prefix: "pre"}
	k, err := d.key("file.txt")
	if err != nil {
		t.Fatalf("key err: %v", err)
	}
	if k != "pre/file.txt" {
		t.Fatalf("key got %q", k)
	}
	if got := d.stripPrefix("pre/dir/file"); got != "dir/file" {
		t.Fatalf("stripPrefix got %q", got)
	}
}

func TestGCSWrapError(t *testing.T) {
	if err := wrapError(storage.ErrObjectNotExist); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
