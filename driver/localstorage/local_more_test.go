package localstorage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/goforj/storage/storagecore"
)

func TestLocalResolvedConfigAndPrefixValidation(t *testing.T) {
	cfg := Config{Root: "/tmp/storage", Prefix: "assets"}
	resolved := cfg.ResolvedConfig()
	if resolved.Remote != "/tmp/storage" || resolved.Prefix != "assets" || resolved.Driver != "local" {
		t.Fatalf("ResolvedConfig = %+v", resolved)
	}

	if _, err := New(Config{Root: t.TempDir(), Prefix: "../bad"}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("New invalid prefix error = %v", err)
	}
}

func TestLocalCRUDBranches(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Prefix: "pre"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := store.(*driver)

	if _, err := d.GetContext(context.Background(), "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("GetContext missing error = %v", err)
	}
	if _, err := d.GetContext(context.Background(), "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("GetContext invalid path error = %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "pre", "folder"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if exists, err := d.ExistsContext(context.Background(), "folder"); err != nil || exists {
		t.Fatalf("ExistsContext dir = %v %v", exists, err)
	}

	if err := d.PutContext(context.Background(), "folder/file.txt", []byte("hello")); err != nil {
		t.Fatalf("PutContext: %v", err)
	}
	if err := d.Put("top.txt", []byte("top")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entry, err := d.StatContext(context.Background(), "folder")
	if err != nil {
		t.Fatalf("StatContext dir: %v", err)
	}
	if !entry.IsDir || entry.Size != 0 {
		t.Fatalf("StatContext dir entry = %+v", entry)
	}

	entry, err = d.Stat("folder/file.txt")
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if entry.Path != "folder/file.txt" || entry.Size != 5 || entry.IsDir {
		t.Fatalf("Stat file entry = %+v", entry)
	}

	entries, err := d.ListContext(context.Background(), "")
	if err != nil {
		t.Fatalf("ListContext root: %v", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"folder", "top.txt"}) {
		t.Fatalf("ListContext paths = %v", paths)
	}

	subEntries, err := d.List("folder")
	if err != nil {
		t.Fatalf("List folder: %v", err)
	}
	if len(subEntries) != 1 || subEntries[0].Path != "folder/file.txt" {
		t.Fatalf("List folder entries = %+v", subEntries)
	}

	if _, err := d.ListContext(context.Background(), "missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("ListContext missing error = %v", err)
	}
	if _, err := d.StatContext(context.Background(), "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("StatContext missing error = %v", err)
	}

	var walked []string
	if err := d.WalkContext(context.Background(), "", func(entry storagecore.Entry) error {
		walked = append(walked, entry.Path)
		return nil
	}); err != nil {
		t.Fatalf("WalkContext dir: %v", err)
	}
	slices.Sort(walked)
	if !slices.Equal(walked, []string{"folder", "folder/file.txt", "top.txt"}) {
		t.Fatalf("WalkContext paths = %v", walked)
	}

	if err := d.DeleteContext(context.Background(), "folder/file.txt"); err != nil {
		t.Fatalf("DeleteContext: %v", err)
	}
	if err := d.Delete("folder/file.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Delete missing error = %v", err)
	}
	if err := d.DeleteContext(context.Background(), "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("DeleteContext invalid path error = %v", err)
	}
	if _, err := d.ListContext(context.Background(), "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ListContext invalid path error = %v", err)
	}
	if err := d.WalkContext(context.Background(), "missing", func(storagecore.Entry) error { return nil }); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("WalkContext missing error = %v", err)
	}
}

func TestLocalCopyAndMoveBranches(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Prefix: "pre"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := store.(*driver)

	if err := d.Put("src.txt", []byte("copy")); err != nil {
		t.Fatalf("Put src: %v", err)
	}
	if err := d.CopyContext(context.Background(), "src.txt", "nested/dst.txt"); err != nil {
		t.Fatalf("CopyContext: %v", err)
	}
	got, err := d.Get("nested/dst.txt")
	if err != nil || string(got) != "copy" {
		t.Fatalf("Get copied = %q err=%v", got, err)
	}

	if err := d.MoveContext(context.Background(), "nested/dst.txt", "moved/out.txt"); err != nil {
		t.Fatalf("MoveContext: %v", err)
	}
	if exists, err := d.Exists("nested/dst.txt"); err != nil || exists {
		t.Fatalf("Exists moved source = %v err=%v", exists, err)
	}

	if err := os.MkdirAll(filepath.Join(root, "pre", "dir"), 0o755); err != nil {
		t.Fatalf("MkdirAll dir: %v", err)
	}
	if err := d.Copy("dir", "copy-dir"); !errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("Copy dir error = %v", err)
	}
	if err := d.Move("dir", "move-dir"); !errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("Move dir error = %v", err)
	}
	if err := d.Copy("missing.txt", "x"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Copy missing error = %v", err)
	}
	if err := d.Move("missing.txt", "x"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Move missing error = %v", err)
	}
	if err := d.Copy("src.txt", "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Copy invalid dst error = %v", err)
	}
	if err := d.Move("src.txt", "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Move invalid dst error = %v", err)
	}
}

func TestLocalListPageContext(t *testing.T) {
	root := t.TempDir()
	store, err := New(Config{Root: root, Prefix: "pre"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := store.(*driver)

	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := d.Put(name, []byte(name)); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
	}

	page, err := d.ListPageContext(context.Background(), "", 0, 2)
	if err != nil {
		t.Fatalf("ListPageContext first: %v", err)
	}
	if !page.HasMore || page.Offset != 0 || page.Limit != 2 {
		t.Fatalf("first page metadata = %+v", page)
	}
	if got := []string{page.Entries[0].Path, page.Entries[1].Path}; !slices.Equal(got, []string{"a.txt", "b.txt"}) {
		t.Fatalf("first page entries = %v", got)
	}

	page, err = d.ListPageContext(context.Background(), "", 2, 2)
	if err != nil {
		t.Fatalf("ListPageContext second: %v", err)
	}
	if page.HasMore {
		t.Fatalf("second page should not have more: %+v", page)
	}
	if len(page.Entries) != 1 || page.Entries[0].Path != "c.txt" {
		t.Fatalf("second page entries = %+v", page.Entries)
	}

	if _, err := d.ListPageContext(context.Background(), "missing", 0, 2); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("ListPageContext missing error = %v", err)
	}
	if _, err := d.ListPageContext(context.Background(), "a.txt", 0, 2); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("ListPageContext file error = %v", err)
	}
}

func TestLocalModTimeAndRelativeEdgeCases(t *testing.T) {
	root := t.TempDir()
	d := &driver{root: root, prefix: "pre"}

	if rel, err := d.userRelative(filepath.Join(root, "pre")); err != nil || rel != "" {
		t.Fatalf("userRelative root = %q err=%v", rel, err)
	}
	if _, err := d.modTime(context.Background(), "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("modTime missing error = %v", err)
	}
}
