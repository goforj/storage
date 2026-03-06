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
	// Walk visits entries recursively when the backend supports it.

	// Example: guard and call walk when supported
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-walk",
	})

	walker, ok := disk.(storage.Walker)
	if !ok {
		fmt.Println("walk unsupported")
		return
	}

	_ = walker.Walk(context.Background(), "", func(entry storage.Entry) error {
		fmt.Println(entry.Path)
		return nil
	})
}
