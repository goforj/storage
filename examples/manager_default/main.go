//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// Default returns the default disk or panics if misconfigured.

	// Example: get the default disk
	mgr, _ := storage.New(storage.Config{
		Default: "local",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"local": localstorage.Config{Remote: "/tmp/storage-default"},
		},
	})

	fs := mgr.Default()
	fmt.Println(fs != nil)
	// Output: true
}
