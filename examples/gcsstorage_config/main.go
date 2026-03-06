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

	// Example: define gcs storage config with all fields
	cfg := gcsstorage.Config{
		Bucket:          "uploads",
		CredentialsJSON: "{...}",              // default: ""
		Endpoint:        "http://127.0.0.1:0", // default: ""
		Prefix:          "assets",             // default: ""
	}
	_ = cfg
}
