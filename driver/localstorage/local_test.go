package localstorage

import (
	"os"
	"path/filepath"
	"testing"
)

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
