//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/storage/driver/sftpstorage"
)

func main() {
	// New constructs SFTP-backed storage using ssh and pkg/sftp.

	// Example: sftp storage
	fs, _ := sftpstorage.New(context.Background(), sftpstorage.Config{
		Host:     "127.0.0.1",
		User:     "demo",
		Password: "secret",
	})
	_ = fs
}
