//go:build ignore
// +build ignore

package main

import "github.com/goforj/storage/driver/ftpstorage"

func main() {
	// Config defines an FTP-backed storage disk.

	// Example: define ftp storage config
	cfg := ftpstorage.Config{
		Host:     "127.0.0.1",
		User:     "demo",
		Password: "secret",
	}
	_ = cfg

	// Example: define ftp storage config with all fields
	cfg := ftpstorage.Config{
		Host:               "127.0.0.1",
		Port:               21,        // default: 21
		User:               "demo",    // default: ""
		Password:           "secret",  // default: ""
		TLS:                false,     // default: false
		InsecureSkipVerify: false,     // default: false
		Prefix:             "uploads", // default: ""
	}
	_ = cfg
}
