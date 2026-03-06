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
	ctx := context.Background()

	t.Run("put-get-exists-delete", func(t *testing.T) {
		path := "dir1/file.txt"
		payload := []byte("hello world")

		if err := fsys.Put(ctx, path, payload); err != nil {
			t.Fatalf("Put: %v", err)
		}

		exists, err := fsys.Exists(ctx, path)
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if !exists {
			t.Fatalf("Exists: expected true")
		}

		got, err := fsys.Get(ctx, path)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != string(payload) {
			t.Fatalf("Get: expected %q got %q", payload, got)
		}

		if err := fsys.Delete(ctx, path); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		exists, err = fsys.Exists(ctx, path)
		if err != nil {
			t.Fatalf("Exists after delete: %v", err)
		}
		if exists {
			t.Fatalf("Exists after delete: expected false")
		}
	})

	t.Run("listing-and-prefix", func(t *testing.T) {
		files := []string{
			"folder1/fileA.txt",
			"folder1/sub/fileB.txt",
			"folder2/fileC.txt",
		}
		for _, f := range files {
			if err := fsys.Put(ctx, f, []byte(f)); err != nil {
				t.Fatalf("Put %q: %v", f, err)
			}
		}

		rootEntries, err := fsys.List(ctx, "")
		if err != nil {
			t.Fatalf("List root: %v", err)
		}
		paths := extractPaths(rootEntries)
		expectRoot := []string{"folder1", "folder2"}
		for _, expect := range expectRoot {
			if !slices.Contains(paths, expect) {
				t.Fatalf("List root missing %q; got %v", expect, paths)
			}
		}

		subEntries, err := fsys.List(ctx, "folder1")
		if err != nil {
			t.Fatalf("List folder1: %v", err)
		}
		subPaths := extractPaths(subEntries)
		expectSub := []string{"folder1/fileA.txt", "folder1/sub"}
		for _, expect := range expectSub {
			if !slices.Contains(subPaths, expect) {
				t.Fatalf("List folder1 missing %q; got %v", expect, subPaths)
			}
		}
	})

	t.Run("walk", func(t *testing.T) {
		walker, ok := fsys.(interface {
			Walk(context.Context, string, func(storage.Entry) error) error
		})
		if !ok {
			t.Skip("Walk not supported; skipping")
		}

		// Seed a small tree that exercises both nested objects and prefixes.
		files := []string{
			"folder1/fileA.txt",
			"folder1/sub/fileB.txt",
			"folder2/fileC.txt",
		}
		for _, f := range files {
			if err := fsys.Put(ctx, f, []byte(f)); err != nil {
				t.Fatalf("Put %q: %v", f, err)
			}
		}

		var walked []string
		if err := walker.Walk(ctx, "", func(entry storage.Entry) error {
			walked = append(walked, entry.Path)
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		for _, expect := range files {
			if !slices.Contains(walked, expect) {
				t.Fatalf("Walk missing %q; got %v", expect, walked)
			}
		}
	})

	t.Run("url-behavior", func(t *testing.T) {
		path := "url/file.txt"
		if err := fsys.Put(ctx, path, []byte("url")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		url, err := fsys.URL(ctx, path)
		if err != nil {
			if !errors.Is(err, storage.ErrUnsupported) {
				t.Fatalf("URL unexpected error: %v", err)
			}
			return
		}
		if url == "" {
			t.Fatalf("URL returned empty string")
		}
	})

	t.Run("error-classification", func(t *testing.T) {
		_, err := fsys.Get(ctx, "missing/file.txt")
		if err == nil || !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("expected ErrNotFound wrapping, got: %v", err)
		}

		err = fsys.Put(ctx, "../escape.txt", []byte("nope"))
		if err == nil || !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("expected ErrForbidden for path traversal, got: %v", err)
		}
	})

	t.Run("context-handling", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := fsys.Put(canceled, "ctx/file.txt", []byte("x")); err == nil {
			t.Fatalf("expected context cancellation")
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
		if err := fsys.Put(ctx, path, []byte("modtime")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		ts, err := mt.ModTime(ctx, path)
		if err != nil {
			t.Fatalf("ModTime: %v", err)
		}
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
