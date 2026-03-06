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
	// Walker is an optional capability for recursive traversal.
	//
	// Walk is not part of the core Storage interface because recursion has very
	// different cost and behavior across backends.

	// Example: check for walk support
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-walk",
	})

	_, ok := disk.(storage.Walker)
	fmt.Println(ok)
	// Output: false
}
