package gcsstorage

import (
	"errors"
	"testing"

	gcsapi "cloud.google.com/go/storage"

	"github.com/goforj/storage"
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
	if err := wrapError(gcsapi.ErrObjectNotExist); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
