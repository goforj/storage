//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// Manager holds named storage disks.

	// Example: keep a manager for later disk lookups
	mgr, _ := storage.New(storage.Config{
		Default: "local",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"local": localstorage.Config{Remote: "/tmp/storage-manager"},
		},
	})
	_ = mgr
}
