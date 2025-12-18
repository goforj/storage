package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestLocalDriverContract(t *testing.T) {
	root := t.TempDir()
	cfg := filesystem.Config{
		Default: "local",
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"local": {
				Driver: "local",
				Remote: root,
				Prefix: "sandbox",
			},
		},
	}

	manager, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	fsys := manager.Default()

	filesystemtest.RunFilesystemContractTests(t, fsys)
}

func TestLocalPrefixIsolation(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	cfg := filesystem.Config{
		Default: "local",
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"local": {
				Driver: "local",
				Remote: root,
				Prefix: "sandbox",
			},
		},
	}
	manager, err := filesystem.New(cfg)
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
