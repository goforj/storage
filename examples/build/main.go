//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// Build constructs a single storage backend from a typed driver config without
	// a Manager.

	// Example: build a single disk
	fs, _ := storage.Build(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-example",
		Prefix: "assets",
	})
	_ = fs
}
