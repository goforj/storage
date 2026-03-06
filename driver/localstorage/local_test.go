package localstorage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/storage"
	storagetest "github.com/goforj/storage/storagetest"
)

func TestLocalDriverContract(t *testing.T) {
	root := t.TempDir()
	cfg := storage.Config{
		Default: "local",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"local": Config{Remote: root, Prefix: "sandbox"},
		},
	}

	manager, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	fsys := manager.Default()

	storagetest.RunStorageContractTests(t, fsys)
}

func TestLocalPrefixIsolation(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	cfg := storage.Config{
		Default: "local",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"local": Config{Remote: root, Prefix: "sandbox"},
		},
	}
	manager, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	fsys := manager.Default()

	ctx := context.Background()
	if err := fsys.Put(ctx, "inside/file.txt", []byte("inside")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, err := fsys.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.Path == "outside.txt" {
			t.Fatalf("prefix isolation failed, saw outside file")
		}
	}
}
