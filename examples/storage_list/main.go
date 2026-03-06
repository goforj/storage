//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// List returns the immediate children under path.

	// Example: list a directory
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-list",
	})
	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))

	entries, _ := disk.List(context.Background(), "docs")
	fmt.Println(entries[0].Path)
	// Output: docs/readme.txt
}
