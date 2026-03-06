//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// New constructs local storage rooted at cfg.Remote with an optional prefix.

	// Example: local storage
	fs, _ := localstorage.New(context.Background(), localstorage.Config{
		Remote: "/tmp/storage-local",
		Prefix: "sandbox",
	})
	_ = fs
}
