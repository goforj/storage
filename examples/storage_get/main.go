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
	// Get reads the object at path.

	// Example: read an object
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-get",
	})
	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))

	data, _ := disk.Get(context.Background(), "docs/readme.txt")
	fmt.Println(string(data))
	// Output: hello
}
