//go:build !integration

package rclonestorage

import (
	"bytes"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestRcloneZstdRoundTrip exercises the configured compression backend with a real compressed file.
func TestRcloneZstdRoundTrip(t *testing.T) {
	resetRcloneInit(t)
	t.Cleanup(func() { resetRcloneInit(t) })
	root := t.TempDir()
	store, err := New(Config{
		Remote:           "compressed:",
		RcloneConfigData: "[compressed]\ntype = compress\nremote = " + root + "\nmode = zstd\nlevel = 2\n",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := store.(io.Closer).Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	payload := bytes.Repeat([]byte("GoForj storage compressed round trip.\n"), 8192)
	if err := store.Put("sample.txt", payload); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get("sample.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("decompressed content differs from original")
	}
	compressed := false
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".zst") {
			compressed = true
		}
		return nil
	}); err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	if !compressed {
		t.Fatal("backend did not write a zstd file")
	}
}
