//go:build ignore
// +build ignore

package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// URL returns a usable access URL when the driver supports it.

	// Example: handle unsupported url generation
	disk, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-url",
	})

	_, err := disk.URL(context.Background(), "docs/readme.txt")
	fmt.Println(errors.Is(err, storage.ErrUnsupported))
	// Output: true
}
