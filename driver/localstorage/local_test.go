package localstorage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// TestLocalStorageBuildAndIO verifies construction and basic local file round trips.
func TestLocalStorageBuildAndIO(t *testing.T) {
	root := t.TempDir()
	fsys, err := New(Config{Root: root, Prefix: "sandbox"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := fsys.Put("file.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := fsys.Get("file.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Get = %q", got)
	}
}

// TestLocalPrefixIsolation verifies configured prefixes remain hidden from logical paths.
func TestLocalPrefixIsolation(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	fsys, err := New(Config{Root: root, Prefix: "sandbox"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := fsys.Put("inside/file.txt", []byte("inside")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, err := fsys.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.Path == "outside.txt" {
			t.Fatalf("prefix isolation failed, saw outside file")
		}
	}
}

// TestLocalStorageWindowsStyleSeparators verifies backslashes normalize to portable logical paths.
func TestLocalStorageWindowsStyleSeparators(t *testing.T) {
	root := t.TempDir()
	fsys, err := New(Config{Root: root, Prefix: "sandbox"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := fsys.Put(`nested\docs\readme.txt`, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	target := filepath.Join(root, "sandbox", "nested", "docs", "readme.txt")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected normalized local path to exist: %v", err)
	}

	got, err := fsys.Get("nested/docs/readme.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Get = %q", got)
	}
}

// TestLocalLogicalRootAndSamePathOperations verifies synthetic-root guards and validated no-op moves.
func TestLocalLogicalRootAndSamePathOperations(t *testing.T) {
	for _, prefix := range []string{"", "sandbox"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			fsys, err := New(Config{Root: t.TempDir(), Prefix: prefix})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if entries, err := fsys.List(""); err != nil || len(entries) != 0 {
				t.Fatalf("List empty root = %+v err=%v", entries, err)
			}
			if err := fsys.Put("file.txt", []byte("payload")); err != nil {
				t.Fatalf("Put seed: %v", err)
			}
			if err := fsys.Copy("file.txt", "file.txt"); err != nil {
				t.Fatalf("Copy same path: %v", err)
			}
			if err := fsys.Move("file.txt", "file.txt"); err != nil {
				t.Fatalf("Move same path: %v", err)
			}
			if data, err := fsys.Get("file.txt"); err != nil || string(data) != "payload" {
				t.Fatalf("Get after same-path operations = %q err=%v", data, err)
			}
			if err := fsys.Move("missing", "missing"); !errors.Is(err, storagecore.ErrNotFound) {
				t.Fatalf("Move missing same path error = %v", err)
			}
			if err := fsys.Copy("missing", "missing"); !errors.Is(err, storagecore.ErrNotFound) {
				t.Fatalf("Copy missing same path error = %v", err)
			}
			for name, err := range map[string]error{
				"put":         fsys.Put("", []byte("root")),
				"copy target": fsys.Copy("file.txt", ""),
				"move source": fsys.Move("", "other"),
				"move target": fsys.Move("file.txt", ""),
				"delete":      fsys.Delete(""),
			} {
				if !errors.Is(err, storagecore.ErrForbidden) {
					t.Errorf("%s root error = %v", name, err)
				}
			}
		})
	}
}
