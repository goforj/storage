//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage/driver/rclonestorage"
)

func main() {
	// S3Remote defines parameters for constructing an rclone S3 remote.

	// Example: define an s3 remote
	remote := rclonestorage.S3Remote{
		Name:            "assets",
		Region:          "us-east-1",
		AccessKeyID:     "key",
		SecretAccessKey: "secret",
	}
	fmt.Println(remote.Name)
	// Output: assets
}
