package storagetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/goforj/storage"
)

// RunStorageContractTests executes the shared contract against any Storage implementation.
func RunStorageContractTests(t *testing.T, fsys storage.Storage) {
	t.Helper()

	t.Run("put-get-exists-delete", func(t *testing.T) {
		path := "dir1/file.txt"
		payload := []byte("hello world")

		requireNoError(t, fsys.Put(path, payload), "Put")

		exists, err := fsys.Exists(path)
		requireNoError(t, err, "Exists")
		requireTrue(t, exists, "Exists: expected true")

		got, err := fsys.Get(path)
		requireNoError(t, err, "Get")
		requireEqual(t, string(payload), string(got), "Get")

		requireNoError(t, fsys.Delete(path), "Delete")

		exists, err = fsys.Exists(path)
		requireNoError(t, err, "Exists after delete")
		requireFalse(t, exists, "Exists after delete: expected false")
	})

	t.Run("stat", func(t *testing.T) {
		path := "stat/file.txt"
		payload := []byte("hello world")

		requireNoError(t, fsys.Put(path, payload), "Put")

		entry, err := fsys.Stat(path)
		requireNoError(t, err, "Stat")
		requireEqual(t, path, entry.Path, "Stat path")
		requireEqual(t, int64(len(payload)), entry.Size, "Stat size")
		requireFalse(t, entry.IsDir, "Stat: expected object entry")
	})

	t.Run("listing-and-prefix", func(t *testing.T) {
		files := []string{
			"folder1/fileA.txt",
			"folder1/sub/fileB.txt",
			"folder2/fileC.txt",
		}
		for _, f := range files {
			requireNoError(t, fsys.Put(f, []byte(f)), "Put "+f)
		}

		rootEntries, err := fsys.List("")
		requireNoError(t, err, "List root")
		paths := extractPaths(rootEntries)
		expectRoot := []string{"folder1", "folder2"}
		for _, expect := range expectRoot {
			requireContains(t, paths, expect, "List root")
		}

		subEntries, err := fsys.List("folder1")
		requireNoError(t, err, "List folder1")
		subPaths := extractPaths(subEntries)
		expectSub := []string{"folder1/fileA.txt", "folder1/sub"}
		for _, expect := range expectSub {
			requireContains(t, subPaths, expect, "List folder1")
		}
	})

	t.Run("walk", func(t *testing.T) {
		// Seed a small tree that exercises both nested objects and prefixes.
		files := []string{
			"folder1/fileA.txt",
			"folder1/sub/fileB.txt",
			"folder2/fileC.txt",
		}
		for _, f := range files {
			requireNoError(t, fsys.Put(f, []byte(f)), "Put "+f)
		}

		var walked []string
		if err := fsys.Walk("", func(entry storage.Entry) error {
			walked = append(walked, entry.Path)
			return nil
		}); err != nil {
			if errors.Is(err, storage.ErrUnsupported) {
				t.Skip("Walk not supported; skipping")
			}
			t.Fatalf("Walk: %v", err)
		}
		for _, expect := range files {
			requireContains(t, walked, expect, "Walk")
		}
	})

	t.Run("copy", func(t *testing.T) {
		src := "copy/source.txt"
		dst := "copy/dest.txt"
		payload := []byte("copied")

		requireNoError(t, fsys.Put(src, payload), "Put")
		requireNoError(t, fsys.Copy(src, dst), "Copy")

		got, err := fsys.Get(dst)
		requireNoError(t, err, "Get copied object")
		requireEqual(t, string(payload), string(got), "Get copied object")

		exists, err := fsys.Exists(src)
		requireNoError(t, err, "Exists source after copy")
		requireTrue(t, exists, "Exists source after copy: expected true")
	})

	t.Run("move", func(t *testing.T) {
		src := "move/source.txt"
		dst := "move/dest.txt"
		payload := []byte("moved")

		requireNoError(t, fsys.Put(src, payload), "Put")
		requireNoError(t, fsys.Move(src, dst), "Move")

		exists, err := fsys.Exists(src)
		requireNoError(t, err, "Exists source after move")
		requireFalse(t, exists, "Exists source after move: expected false")

		got, err := fsys.Get(dst)
		requireNoError(t, err, "Get moved object")
		requireEqual(t, string(payload), string(got), "Get moved object")
	})

	t.Run("url-behavior", func(t *testing.T) {
		path := "url/file.txt"
		requireNoError(t, fsys.Put(path, []byte("url")), "Put")
		url, err := fsys.URL(path)
		if err != nil {
			if !errors.Is(err, storage.ErrUnsupported) {
				t.Fatalf("URL unexpected error: %v", err)
			}
			return
		}
		requireTrue(t, url != "", "URL returned empty string")
	})

	t.Run("error-classification", func(t *testing.T) {
		_, err := fsys.Get("missing/file.txt")
		requireErrorIs(t, err, storage.ErrNotFound, "expected ErrNotFound wrapping")

		err = fsys.Put("../escape.txt", []byte("nope"))
		requireErrorIs(t, err, storage.ErrForbidden, "expected ErrForbidden for path traversal")
	})

	t.Run("context-handling", func(t *testing.T) {
		csys, ok := fsys.(storage.ContextStorage)
		if !ok {
			t.Skip("ContextStorage not supported; skipping")
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := csys.GetContext(canceled, "ctx/file.txt"); err == nil {
			t.Fatalf("expected context cancellation from GetContext")
		}
		if err := csys.PutContext(canceled, "ctx/file.txt", []byte("x")); err == nil {
			t.Fatalf("expected context cancellation")
		}
		if err := csys.DeleteContext(canceled, "ctx/file.txt"); err == nil {
			t.Fatalf("expected context cancellation from DeleteContext")
		}
		if _, err := csys.StatContext(canceled, "ctx/file.txt"); err == nil {
			t.Fatalf("expected context cancellation from StatContext")
		}
		if _, err := csys.ExistsContext(canceled, "ctx/file.txt"); err == nil {
			t.Fatalf("expected context cancellation from ExistsContext")
		}
		if _, err := csys.ListContext(canceled, "ctx"); err == nil {
			t.Fatalf("expected context cancellation from ListContext")
		}
		if err := csys.WalkContext(canceled, "ctx", func(storage.Entry) error { return nil }); err == nil {
			t.Fatalf("expected context cancellation from WalkContext")
		}
		if err := csys.CopyContext(canceled, "ctx/file.txt", "ctx/file-copy.txt"); err == nil {
			t.Fatalf("expected context cancellation from CopyContext")
		}
		if err := csys.MoveContext(canceled, "ctx/file.txt", "ctx/file-move.txt"); err == nil {
			t.Fatalf("expected context cancellation from MoveContext")
		}
		if _, err := csys.URLContext(canceled, "ctx/file.txt"); err == nil {
			t.Fatalf("expected context cancellation from URLContext")
		}
	})

	t.Run("modtime", func(t *testing.T) {
		mt, ok := fsys.(interface {
			ModTime(context.Context, string) (time.Time, error)
		})
		if !ok {
			t.Skip("ModTime not supported; skipping")
		}

		now := time.Now().UTC()
		path := "modtime/file.txt"
		requireNoError(t, fsys.Put(path, []byte("modtime")), "Put")
		ts, err := mt.ModTime(context.Background(), path)
		requireNoError(t, err, "ModTime")
		if delta := ts.Sub(now); delta < -2*time.Second || delta > 2*time.Second {
			t.Fatalf("modtime out of expected range: got %v, now %v", ts, now)
		}
	})
}

func extractPaths(entries []storage.Entry) []string {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	return paths
}

func requireNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

func requireTrue(t *testing.T, cond bool, msg string) {
	t.Helper()
	if !cond {
		t.Fatal(msg)
	}
}

func requireFalse(t *testing.T, cond bool, msg string) {
	t.Helper()
	if cond {
		t.Fatal(msg)
	}
}

func requireEqual[T comparable](t *testing.T, want, got T, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: expected %v got %v", msg, want, got)
	}
}

func requireContains(t *testing.T, values []string, want string, msg string) {
	t.Helper()
	if !slices.Contains(values, want) {
		t.Fatalf("%s missing %q; got %v", msg, want, values)
	}
}

func requireErrorIs(t *testing.T, err error, target error, msg string) {
	t.Helper()
	if err == nil || !errors.Is(err, target) {
		t.Fatalf("%s, got: %v", msg, err)
	}
}
