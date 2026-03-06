//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/storage/driver/dropboxstorage"
)

func main() {
	// New constructs Dropbox-backed storage using the official SDK.

	// Example: dropbox storage
	fs, _ := dropboxstorage.New(context.Background(), dropboxstorage.Config{
		Token: "token",
	})
	_ = fs
}
