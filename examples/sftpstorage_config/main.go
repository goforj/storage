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

	// Example: define sftp storage config with all fields
	cfg := sftpstorage.Config{
		Host:                  "127.0.0.1",
		Port:                  22,                  // default: 22
		User:                  "demo",              // default: "root"
		Password:              "secret",            // default: ""
		KeyPath:               "/path/id_ed25519",  // default: ""
		KnownHostsPath:        "/path/known_hosts", // default: ""
		InsecureIgnoreHostKey: false,               // default: false
		Prefix:                "uploads",           // default: ""
	}
	_ = cfg
}
