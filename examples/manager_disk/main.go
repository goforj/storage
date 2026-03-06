//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// Disk returns a named disk or an error if it does not exist.

	// Example: get a named disk
	mgr, _ := storage.New(storage.Config{
		Default: "local",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"local":   localstorage.Config{Remote: "/tmp/storage-default"},
			"uploads": localstorage.Config{Remote: "/tmp/storage-uploads"},
		},
	})

	fs, _ := mgr.Disk("uploads")
	fmt.Println(fs != nil)
	// Output: true
}
