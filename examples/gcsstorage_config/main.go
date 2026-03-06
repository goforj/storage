//go:build ignore
// +build ignore

package main

import "github.com/goforj/storage/driver/gcsstorage"

func main() {
	// Config defines a GCS-backed storage disk.

	// Example: define gcs storage config
	cfg := gcsstorage.Config{
		Bucket: "uploads",
	}
	_ = cfg
}
