//go:build ignore
// +build ignore

package main

import "github.com/goforj/storage/driver/localstorage"

func main() {
	// Config defines local storage rooted at a filesystem path.

	// Example: define local storage config
	cfg := localstorage.Config{
		Remote: "/tmp/storage-local",
		Prefix: "sandbox",
	}
	_ = cfg

	// Example: define local storage config with all fields
	cfg := localstorage.Config{
		Remote: "/tmp/storage-local",
		Prefix: "sandbox", // default: ""
	}
	_ = cfg
}
