//go:build linux

package localstorage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestRootedRenameRejectsFIFOParent verifies parent resolution requires a
// directory and cannot block by opening a special file as an ordinary reader.
func TestRootedRenameRejectsFIFOParent(t *testing.T) {
	directory := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(directory, "pipe"), 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	result := make(chan error, 1)
	go func() {
		result <- rootedRename(root, "pipe/child", "target")
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("rootedRename accepted a FIFO as a parent directory")
		}
	case <-time.After(time.Second):
		t.Fatal("rootedRename blocked while resolving a FIFO parent")
	}
}
