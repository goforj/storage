package localstorage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// TestLocalResolvedConfigAndPrefixValidation verifies config mapping and prefix traversal rejection.
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

// TestLocalCRUDBranches verifies object, directory, listing, and deletion edge cases.
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

// TestLocalCopyAndMoveBranches verifies atomic replacement, same-path validation, and missing sources.
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
	if err := d.Move("dir", "move-dir"); err != nil {
		t.Fatalf("Move dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pre", "move-dir")); err != nil {
		t.Fatalf("move dir stat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pre", "dir")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source dir should be gone, err=%v", err)
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

// TestLocalListPageContext verifies pagination bounds over the sorted local listing.
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

// TestLocalModTimeAndRelativeEdgeCases verifies metadata errors and logical path projection.
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

// TestLocalInvalidPathsAndThinWrappers covers validation before filesystem
// access and the background-context adapters omitted by the shared contract.
func TestLocalInvalidPathsAndThinWrappers(t *testing.T) {
	d := &driver{}
	calls := []func() error{
		func() error { _, err := d.GetContext(nil, "../bad"); return err },
		func() error { return d.PutContext(nil, "../bad", nil) },
		func() error { return d.MakeDirContext(nil, "../bad") },
		func() error { return d.DeleteContext(nil, "../bad") },
		func() error { _, err := d.StatContext(nil, "../bad"); return err },
		func() error { _, err := d.ExistsContext(nil, "../bad"); return err },
		func() error { _, err := d.ListContext(nil, "../bad"); return err },
		func() error { _, err := d.ListPageContext(nil, "../bad", 0, 1); return err },
		func() error { return d.WalkContext(nil, "../bad", func(storagecore.Entry) error { return nil }) },
		func() error { return d.CopyContext(nil, "../bad", "dst") },
		func() error { return d.CopyContext(nil, "src", "../bad") },
		func() error { return d.MoveContext(nil, "../bad", "dst") },
		func() error { return d.MoveContext(nil, "src", "../bad") },
	}
	for index, call := range calls {
		if err := call(); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("invalid-path call %d error = %v", index, err)
		}
	}
	if err := d.MakeDir("../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("MakeDir invalid path error = %v", err)
	}
	if _, err := d.ListPage("../bad", 0, 1); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ListPage invalid path error = %v", err)
	}
}

// TestLocalFilesystemAndStreamFailures covers unavailable roots and atomic
// transfer failures without depending on platform-specific permissions.
func TestLocalFilesystemAndStreamFailures(t *testing.T) {
	t.Run("unavailable root", func(t *testing.T) {
		d := &driver{root: filepath.Join(t.TempDir(), "missing")}
		calls := []func() error{
			func() error { _, err := d.Get("file"); return err },
			func() error { return d.Put("file", nil) },
			func() error { return d.MakeDir("dir") },
			func() error { return d.Delete("file") },
			func() error { _, err := d.Stat("file"); return err },
			func() error { _, err := d.Exists("file"); return err },
			func() error { _, err := d.List(""); return err },
			func() error { return d.Walk("", func(storagecore.Entry) error { return nil }) },
			func() error { return d.Copy("file", "copy") },
			func() error { return d.Move("file", "moved") },
			func() error { _, err := d.modTime(context.Background(), "file"); return err },
		}
		for index, call := range calls {
			if err := call(); err == nil {
				t.Fatalf("unavailable-root call %d returned nil error", index)
			}
		}
	})

	t.Run("stream helpers", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := copyContext(ctx, io.Discard, &localErrorReader{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("copyContext cancellation error = %v", err)
		}
		readErr := errors.New("read")
		if err := copyContext(context.Background(), io.Discard, &localErrorReader{err: readErr}); !errors.Is(err, readErr) {
			t.Fatalf("copyContext read error = %v", err)
		}
		writeErr := errors.New("write")
		if err := copyContext(context.Background(), &localFailingWriter{err: writeErr}, &localDataReader{}); !errors.Is(err, writeErr) {
			t.Fatalf("copyContext write error = %v", err)
		}
		if err := copyContext(context.Background(), &localShortWriter{}, &localDataReader{}); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("copyContext short write error = %v", err)
		}
		primary := errors.New("primary")
		cleanup := errors.New("cleanup")
		if err := joinCleanup(primary, cleanup); !errors.Is(err, primary) || !errors.Is(err, cleanup) {
			t.Fatalf("joinCleanup error = %v", err)
		}
		if err := joinCleanup(nil, cleanup); !errors.Is(err, cleanup) {
			t.Fatalf("joinCleanup cleanup error = %v", err)
		}
	})

	t.Run("atomic cancellation and helper projections", func(t *testing.T) {
		rootPath := t.TempDir()
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		defer root.Close()
		ctx, cancel := context.WithCancel(context.Background())
		err = atomicWrite(ctx, root, "file", 0o644, func(file *os.File) error {
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("atomicWrite cancellation error = %v", err)
		}
		d := &driver{root: rootPath, prefix: "pre"}
		if _, err := d.displayName("outside/file"); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("displayName outside error = %v", err)
		}
		if mode := existingMode(root, "missing", 0o600); mode != 0o600 {
			t.Fatalf("existingMode fallback = %v", mode)
		}
	})
}

// TestLocalConstructorAndAtomicEdges covers setup failures and operation
// cancellation at filesystem boundaries using only temporary directories.
func TestLocalConstructorAndAtomicEdges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewContext(ctx, Config{Root: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewContext canceled error = %v", err)
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("New missing root returned nil error")
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile root: %v", err)
	}
	if _, err := New(Config{Root: fileRoot}); err == nil {
		t.Fatal("New file root returned nil error")
	}
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "pre"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile prefix: %v", err)
	}
	if _, err := New(Config{Root: rootPath, Prefix: "pre"}); err == nil {
		t.Fatal("New colliding prefix returned nil error")
	}

	rootPath = t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()
	writeErr := errors.New("write")
	if err := atomicWrite(context.Background(), root, "write-failure", 0o600, func(*os.File) error { return writeErr }); !errors.Is(err, writeErr) {
		t.Fatalf("atomicWrite callback error = %v", err)
	}
	if err := atomicWrite(context.Background(), root, "missing/file", 0o600, func(*os.File) error { return nil }); err == nil {
		t.Fatal("atomicWrite missing parent returned nil error")
	}
	if err := root.Mkdir("target", 0o755); err != nil {
		t.Fatalf("Mkdir target: %v", err)
	}
	if err := atomicWrite(context.Background(), root, "target", 0o600, func(file *os.File) error {
		_, err := file.Write([]byte("x"))
		return err
	}); err == nil {
		t.Fatal("atomicWrite directory target returned nil error")
	}
	if mode := existingMode(root, "target", 0o640); mode != 0o640 {
		t.Fatalf("existingMode directory fallback = %v", mode)
	}
}

// TestLocalFilesystemCollisionEdges verifies ordinary filesystem collisions
// remain visible through the portable storage error identities.
func TestLocalFilesystemCollisionEdges(t *testing.T) {
	rootPath := t.TempDir()
	store, err := New(Config{Root: rootPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := store.(*driver)
	if err := d.Put("source", []byte("source")); err != nil {
		t.Fatalf("Put source: %v", err)
	}
	if err := d.Put("blocker", []byte("blocker")); err != nil {
		t.Fatalf("Put blocker: %v", err)
	}

	calls := []struct {
		name string
		call func() error
	}{
		{name: "put beneath file", call: func() error { return d.Put("blocker/child", nil) }},
		{name: "mkdir beneath file", call: func() error { return d.MakeDir("blocker/child") }},
		{name: "copy beneath file", call: func() error { return d.Copy("source", "blocker/copy") }},
		{name: "move beneath file", call: func() error { return d.Move("source", "blocker/move") }},
	}
	for _, test := range calls {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("operation returned nil error")
			}
		})
	}

	if err := d.MakeDir(""); err != nil {
		t.Fatalf("MakeDir root: %v", err)
	}
	if err := d.MakeDir("nonempty"); err != nil {
		t.Fatalf("MakeDir nonempty: %v", err)
	}
	if err := d.Put("nonempty/child", nil); err != nil {
		t.Fatalf("Put nonempty child: %v", err)
	}
	if err := d.Delete("nonempty"); err == nil {
		t.Fatalf("Delete nonempty directory error = %v", err)
	}
	if _, err := d.Get("nonempty"); err == nil {
		t.Fatal("Get directory returned nil error")
	}
	if err := d.Copy("source", "source"); err != nil {
		t.Fatalf("Copy same path: %v", err)
	}
	if err := d.Move("source", "source"); err != nil {
		t.Fatalf("Move same path: %v", err)
	}
	if err := d.Walk("", nil); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Walk nil callback error = %v", err)
	}
	if _, err := d.userRelative(filepath.Join(filepath.Dir(rootPath), "outside")); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("userRelative outside error = %v", err)
	}
	if _, err := d.modTime(context.Background(), "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("modTime invalid path error = %v", err)
	}
}

// TestLocalAtomicPostWriteEdges covers cancellation after synchronization and
// direct file-write failures without installing incomplete content.
func TestLocalAtomicPostWriteEdges(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	ctx := &stepCancelContext{Context: context.Background(), cancelAt: 2}
	if err := atomicWrite(ctx, root, "canceled", 0o600, func(file *os.File) error {
		_, err := file.Write([]byte("data"))
		return err
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("atomicWrite post-sync cancellation error = %v", err)
	}
	if _, err := root.Stat("canceled"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled target stat error = %v", err)
	}

	file, err := os.CreateTemp(rootPath, "closed-")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close temporary file: %v", err)
	}
	if err := writeBytesContext(context.Background(), file, []byte("data")); err == nil {
		t.Fatal("writeBytesContext closed file returned nil error")
	}
	if err := writeBytesContext(&stepCancelContext{Context: context.Background(), cancelAt: 1}, file, []byte("data")); !errors.Is(err, context.Canceled) {
		t.Fatalf("writeBytesContext cancellation error = %v", err)
	}
}

// TestLocalMidOperationCancellation verifies long-running filesystem methods
// observe cancellation after their initial validation check.
func TestLocalMidOperationCancellation(t *testing.T) {
	root := t.TempDir()
	d := &driver{root: root}
	if err := d.Put("a", []byte("a")); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := d.Put("b", []byte("b")); err != nil {
		t.Fatalf("Put b: %v", err)
	}

	if _, err := d.GetContext(&stepCancelContext{Context: context.Background(), cancelAt: 3}, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetContext mid-read cancellation = %v", err)
	}
	if _, err := d.ListContext(&stepCancelContext{Context: context.Background(), cancelAt: 2}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext mid-list cancellation = %v", err)
	}
	if err := d.WalkContext(&stepCancelContext{Context: context.Background(), cancelAt: 3}, "", func(storagecore.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext mid-walk cancellation = %v", err)
	}
	if err := d.MoveContext(&stepCancelContext{Context: context.Background(), cancelAt: 2}, "a", "nested/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext pre-rename cancellation = %v", err)
	}
}

type stepCancelContext struct {
	context.Context
	calls    int
	cancelAt int
}

// Err begins returning context.Canceled at the configured observation count.
func (c *stepCancelContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

type localErrorReader struct {
	err error
}

// Read returns the configured failure without producing data.
func (r *localErrorReader) Read([]byte) (int, error) {
	if r.err == nil {
		return 0, io.EOF
	}
	return 0, r.err
}

type localDataReader struct {
	done bool
}

// Read produces one byte before ending the stream.
func (r *localDataReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	p[0] = 'x'
	return 1, nil
}

type localFailingWriter struct {
	err error
}

// Write injects the configured destination failure.
func (w *localFailingWriter) Write([]byte) (int, error) { return 0, w.err }

type localShortWriter struct{}

// Write accepts no bytes without an error to exercise short-write handling.
func (*localShortWriter) Write([]byte) (int, error) { return 0, nil }
