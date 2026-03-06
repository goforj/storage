//go:build ignore
// +build ignore

package main

import "github.com/goforj/storage/driver/dropboxstorage"

func main() {
	// Config defines a Dropbox-backed storage disk.

	// Example: define dropbox storage config
	cfg := dropboxstorage.Config{
		Token: "token",
	}
	_ = cfg
}
