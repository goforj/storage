//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// Storage is the public interface for interacting with a storage backend.
	//
	// Semantics:
	//   - Put overwrites an existing object at the same path.
	//   - List is one-level and non-recursive.
	//   - List with an empty path lists from the disk root or prefix root.
	//   - URL returns a usable access URL when the driver supports it.
	//   - Unsupported operations should return ErrUnsupported.

	// Example: use the storage interface
	var disk storage.Storage
	disk, _ = storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-interface",
	})
	_ = disk
}
