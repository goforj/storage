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
	// Exists reports whether an object exists at path.

	// Example: check for an object
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-exists",
	})
	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))

	ok, _ := disk.Exists(context.Background(), "docs/readme.txt")
	fmt.Println(ok)
	// Output: true
}
