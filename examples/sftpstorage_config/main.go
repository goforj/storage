//go:build ignore
// +build ignore

package main

import "github.com/goforj/storage/driver/sftpstorage"

func main() {
	// Config defines an SFTP-backed storage disk.

	// Example: define sftp storage config
	cfg := sftpstorage.Config{
		Host:     "127.0.0.1",
		User:     "demo",
		Password: "secret",
	}
	_ = cfg
}
