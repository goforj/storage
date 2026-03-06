//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	// DriverConfig is implemented by typed driver configs such as local.Config or
	// s3storage.Config. It is the public config boundary for Manager and Build.

	// Example: pass a typed driver config
	var cfg storage.DriverConfig = localstorage.Config{
		Remote: "/tmp/storage-config",
	}
	_ = cfg
}
