//go:build ignore
// +build ignore

package main

import "github.com/goforj/storage/driver/rclonestorage"

func main() {
	// Config defines an rclone-backed storage disk.

	// Example: define rclone storage config
	cfg := rclonestorage.Config{
		Remote: "local:",
		Prefix: "sandbox",
	}
	_ = cfg
}
