//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/storage/driver/gcsstorage"
)

func main() {
	// New constructs GCS-backed storage using cloud.google.com/go/storage.

	// Example: gcs storage
	fs, _ := gcsstorage.New(context.Background(), gcsstorage.Config{
		Bucket: "uploads",
	})
	_ = fs
}
