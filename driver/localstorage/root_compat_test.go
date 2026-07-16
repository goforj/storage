package localstorage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// TestRootedMkdirAll verifies nested creation, idempotence, and file collision handling.
func TestRootedMkdirAll(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	if err := rootedMkdirAll(root, "one/two/three", 0o755); err != nil {
		t.Fatalf("rootedMkdirAll nested: %v", err)
	}
	if err := rootedMkdirAll(root, "one/two/three", 0o755); err != nil {
		t.Fatalf("rootedMkdirAll existing: %v", err)
	}
	info, err := os.Stat(filepath.Join(directory, "one", "two", "three"))
	if err != nil || !info.IsDir() {
		t.Fatalf("nested directory info = %+v, err = %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "one", "file"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile collision: %v", err)
	}
	if err := rootedMkdirAll(root, "one/file/child", 0o755); err == nil {
		t.Fatal("rootedMkdirAll accepted a file as a parent directory")
	}
	if err := rootedMkdirAll(root, "../outside", 0o755); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("rootedMkdirAll traversal error = %v", err)
	}

	closedRoot, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot for closed handle: %v", err)
	}
	if err := closedRoot.Close(); err != nil {
		t.Fatalf("Close root: %v", err)
	}
	if err := rootedMkdirAll(closedRoot, "closed", 0o755); err == nil {
		t.Fatal("rootedMkdirAll accepted a closed Root")
	}
}

// TestRootedRename verifies atomic replacement and cross-directory moves through rooted handles.
func TestRootedRename(t *testing.T) {
	if runtime.GOOS == "plan9" {
		t.Skip("Plan 9 does not support cross-directory rename")
	}
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "from"), 0o755); err != nil {
		t.Fatalf("MkdirAll from: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(directory, "to"), 0o755); err != nil {
		t.Fatalf("MkdirAll to: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "from", "source"), []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "to", "target"), []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	if err := rootedRename(root, "from/source", "to/target"); err != nil {
		t.Fatalf("rootedRename: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "to", "target"))
	if err != nil || string(contents) != "new" {
		t.Fatalf("replacement contents = %q, err = %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "from", "source")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remains after rename: %v", err)
	}
	if err := rootedRename(root, "../source", "to/target"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("rootedRename old traversal error = %v", err)
	}
	if err := rootedRename(root, "to/target", "../target"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("rootedRename new traversal error = %v", err)
	}
	if err := rootedRename(root, "missing/source", "to/target"); err == nil {
		t.Fatal("rootedRename accepted a missing source parent")
	}
	if err := rootedRename(root, "to/missing", "from/target"); err == nil {
		t.Fatal("rootedRename accepted a missing source")
	}
}

// TestRootedRenameReplacesFinalSymlink verifies replacement mutates the link
// entry itself without following its target beyond the storage boundary.
func TestRootedRenameReplacesFinalSymlink(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "target")
	if err := os.WriteFile(outsideTarget, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile outside target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("inside"), 0o644); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	destination := filepath.Join(directory, "destination")
	if err := os.Symlink(outsideTarget, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	if err := rootedRename(root, "source", "destination"); err != nil {
		t.Fatalf("rootedRename final symlink: %v", err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "inside" {
		t.Fatalf("destination contents = %q, err = %v", contents, err)
	}
	outsideContents, err := os.ReadFile(outsideTarget)
	if err != nil || string(outsideContents) != "outside" {
		t.Fatalf("outside target contents = %q, err = %v", outsideContents, err)
	}
	info, err := os.Lstat(destination)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("destination info = %+v, err = %v", info, err)
	}
}

// TestRootedRenameRejectsEscapingSymlink verifies destination resolution cannot leave Root.
func TestRootedRenameRejectsEscapingSymlink(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("inside"), 0o644); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	outsideTarget := filepath.Join(outside, "target")
	if err := os.WriteFile(outsideTarget, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile outside target: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	err = wrapLocalError(rootedRename(root, "source", "escape/target"))
	if !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("rootedRename escape error = %v", err)
	}
	contents, readErr := os.ReadFile(outsideTarget)
	if readErr != nil || string(contents) != "outside" {
		t.Fatalf("outside target contents = %q, err = %v", contents, readErr)
	}
}

// TestRootedMkdirAllRejectsEscapingSymlink verifies nested creation cannot follow a link outside Root.
func TestRootedMkdirAllRejectsEscapingSymlink(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	err = wrapLocalError(rootedMkdirAll(root, "escape/child", 0o755))
	if !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("rootedMkdirAll escape error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside child was created: %v", err)
	}
}

// TestValidateRenameNameRejectsAmbiguousPaths verifies parent extraction cannot clean unsafe syntax.
func TestValidateRenameNameRejectsAmbiguousPaths(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../outside", "/absolute", "one/../two", `one\two`, "nul\x00byte"} {
		if err := validateRenameName(name); !errors.Is(err, storagecore.ErrForbidden) {
			t.Errorf("validateRenameName(%q) error = %v", name, err)
		}
	}
}
