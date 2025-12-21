package ftpdriver

import (
	"errors"
	"testing"

	"github.com/goforj/filesystem"
)

func TestFTPPrefixHelpers(t *testing.T) {
	d := &Driver{prefix: "pre"}
	if got := d.stripPrefix("pre/path/to"); got != "path/to" {
		t.Fatalf("stripPrefix got %q", got)
	}
}

func TestFTPWrapError(t *testing.T) {
	if err := wrapError(errors.New("file not found")); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := wrapError(errors.New("File not available")); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for case-insensitive match")
	}
}
