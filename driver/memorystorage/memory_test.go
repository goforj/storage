package memorystorage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/memorystorage"
)

func TestConfigResolvedConfig(t *testing.T) {
	cfg := memorystorage.Config{Prefix: "sandbox"}
	if got := cfg.DriverName(); got != "memory" {
		t.Fatalf("DriverName = %q", got)
	}
	resolved := cfg.ResolvedConfig()
	if resolved.Driver != "memory" {
		t.Fatalf("Driver = %q", resolved.Driver)
	}
	if resolved.Prefix != "sandbox" {
		t.Fatalf("Prefix = %q", resolved.Prefix)
	}
}

func TestMemoryStorageBuildAndIO(t *testing.T) {
	store, err := memorystorage.New(memorystorage.Config{Prefix: "itest"})
	if err != nil {
		t.Fatalf("memorystorage.New: %v", err)
	}
	if err := store.Put("file.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get("file.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Get = %q", got)
	}
}

func TestContextCancellation(t *testing.T) {
	store, err := memorystorage.New(memorystorage.Config{})
	if err != nil {
		t.Fatalf("memorystorage.New: %v", err)
	}
	d, ok := store.(storage.ContextStorage)
	if !ok {
		t.Fatal("store does not implement storage.ContextStorage")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := d.GetContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetContext error = %v", err)
	}
	if err := d.PutContext(ctx, "file.txt", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext error = %v", err)
	}
	if err := d.DeleteContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext error = %v", err)
	}
	if _, err := d.StatContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("StatContext error = %v", err)
	}
	if _, err := d.ExistsContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExistsContext error = %v", err)
	}
	if _, err := d.ListContext(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext error = %v", err)
	}
	if err := d.WalkContext(ctx, "", func(storage.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext error = %v", err)
	}
	if err := d.CopyContext(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext error = %v", err)
	}
	if err := d.MoveContext(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext error = %v", err)
	}
	if _, err := d.URLContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
}

func TestDirectoryStatAndList(t *testing.T) {
	store, err := memorystorage.New(memorystorage.Config{})
	if err != nil {
		t.Fatalf("memorystorage.New: %v", err)
	}

	if err := store.Put("dir/sub/file.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entry, err := store.Stat("dir")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !entry.IsDir || entry.Path != "dir" {
		t.Fatalf("Stat dir entry = %+v", entry)
	}
	entries, err := store.List("dir")
	if err != nil {
		t.Fatalf("List dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "dir/sub" || !entries[0].IsDir {
		t.Fatalf("List dir entries = %+v", entries)
	}
}

func TestModTime(t *testing.T) {
	store, err := memorystorage.New(memorystorage.Config{})
	if err != nil {
		t.Fatalf("memorystorage.New: %v", err)
	}
	if err := store.Put("file.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	mt, ok := store.(interface {
		ModTime(context.Context, string) (time.Time, error)
	})
	if !ok {
		t.Fatal("store does not implement ModTime")
	}
	got, err := mt.ModTime(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("ModTime: %v", err)
	}
	if got.IsZero() {
		t.Fatal("ModTime returned zero")
	}
	if time.Since(got) > time.Minute {
		t.Fatalf("ModTime too old: %v", got)
	}

	if _, err := mt.ModTime(context.Background(), "missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ModTime missing error = %v", err)
	}
}

func TestMemoryStorageEdgeCases(t *testing.T) {
	store, err := memorystorage.New(memorystorage.Config{Prefix: "pre"})
	if err != nil {
		t.Fatalf("memorystorage.New: %v", err)
	}

	if _, err := memorystorage.New(memorystorage.Config{Prefix: "../bad"}); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("New invalid prefix error = %v", err)
	}
	if _, err := store.Get("missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get missing error = %v", err)
	}
	if err := store.Delete("missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Delete missing error = %v", err)
	}
	if _, err := store.Stat("missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat missing error = %v", err)
	}
	if _, err := store.List("missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("List missing error = %v", err)
	}
	if err := store.Walk("missing", func(storage.Entry) error { return nil }); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Walk missing error = %v", err)
	}
	if _, err := store.URL("missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("URL missing error = %v", err)
	}
	if err := store.Copy("missing.txt", "copy.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Copy missing error = %v", err)
	}
	if err := store.Move("missing.txt", "move.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Move missing error = %v", err)
	}
}

func TestMemoryStorageListWalkCopyMoveURL(t *testing.T) {
	store, err := memorystorage.New(memorystorage.Config{})
	if err != nil {
		t.Fatalf("memorystorage.New: %v", err)
	}

	payload := []byte("hello")
	if err := store.Put("dir/sub/file.txt", payload); err != nil {
		t.Fatalf("Put: %v", err)
	}
	payload[0] = 'x'
	got, err := store.Get("dir/sub/file.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Get cloned payload = %q", got)
	}
	got[0] = 'y'
	got2, err := store.Get("dir/sub/file.txt")
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if string(got2) != "hello" {
		t.Fatalf("Get second payload = %q", got2)
	}

	entries, err := store.List("dir/sub/file.txt")
	if err == nil || !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("List file error = %v entries=%v", err, entries)
	}

	var walked []string
	stop := errors.New("stop")
	err = store.Walk("dir", func(entry storage.Entry) error {
		walked = append(walked, entry.Path)
		if entry.Path == "dir/sub/file.txt" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Walk callback error = %v", err)
	}
	if len(walked) == 0 {
		t.Fatal("Walk returned no entries")
	}

	if err := store.Copy("dir/sub/file.txt", "copy.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	copyData, err := store.Get("copy.txt")
	if err != nil || string(copyData) != "hello" {
		t.Fatalf("Get copy = %q err=%v", copyData, err)
	}

	if err := store.Move("copy.txt", "moved.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if exists, err := store.Exists("copy.txt"); err != nil || exists {
		t.Fatalf("Exists old copy = %v err=%v", exists, err)
	}

	if _, err := store.URL("moved.txt"); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("URL moved error = %v", err)
	}
}
