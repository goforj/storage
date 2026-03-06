//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// New constructs a Manager and eagerly initializes all disks.

	// Example: build a manager with named disks
	mgr, _ := storage.New(storage.Config{
		Default: "local",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"local":  localstorage.Config{Remote: "/tmp/storage-local"},
			"assets": localstorage.Config{Remote: "/tmp/storage-assets", Prefix: "public"},
		},
	})
	_ = mgr
}
