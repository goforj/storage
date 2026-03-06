package memorystorage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/storage"
	"github.com/goforj/storage/storagetest"
)

func TestConfigResolvedConfig(t *testing.T) {
	cfg := Config{Prefix: "sandbox"}
	resolved := cfg.ResolvedConfig()
	if resolved.Driver != "memory" {
		t.Fatalf("Driver = %q", resolved.Driver)
	}
	if resolved.Prefix != "sandbox" {
		t.Fatalf("Prefix = %q", resolved.Prefix)
	}
}

func TestMemoryStorageContract(t *testing.T) {
	store, err := New(Config{Prefix: "itest"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	storagetest.RunStorageContractTests(t, store)
}

func TestContextCancellation(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := store.(*driver)

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
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := store.(*driver)

	if err := d.Put("dir/sub/file.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entry, err := d.Stat("dir")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !entry.IsDir || entry.Path != "dir" {
		t.Fatalf("Stat dir entry = %+v", entry)
	}
	entries, err := d.List("dir")
	if err != nil {
		t.Fatalf("List dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "dir/sub" || !entries[0].IsDir {
		t.Fatalf("List dir entries = %+v", entries)
	}
}

func TestModTime(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := store.(*driver)
	if err := d.Put("file.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := d.ModTime(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("ModTime: %v", err)
	}
	if got.IsZero() {
		t.Fatal("ModTime returned zero")
	}
	if time.Since(got) > time.Minute {
		t.Fatalf("ModTime too old: %v", got)
	}
}
