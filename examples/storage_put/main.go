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
	// Put writes an object at path, overwriting any existing object.

	// Example: write an object
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-put",
	})
	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
	fmt.Println("stored")
	// Output: stored
}
