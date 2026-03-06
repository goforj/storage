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
	// Delete removes the object at path.

	// Example: delete an object
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-delete",
	})
	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
	_ = disk.Delete(context.Background(), "docs/readme.txt")

	ok, _ := disk.Exists(context.Background(), "docs/readme.txt")
	fmt.Println(ok)
	// Output: false
}
