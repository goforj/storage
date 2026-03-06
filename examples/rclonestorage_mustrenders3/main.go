//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage/driver/rclonestorage"
)

func main() {
	// MustRenderS3 panics on error.

	// Example: render an s3 remote without handling the error
	cfg := rclonestorage.MustRenderS3(rclonestorage.S3Remote{
		Name:            "assets",
		Region:          "us-east-1",
		AccessKeyID:     "key",
		SecretAccessKey: "secret",
	})
	fmt.Println(cfg)
	// Output:
	// [assets]
	// type = s3
	// provider = AWS
	// access_key_id = key
	// secret_access_key = secret
	// region = us-east-1
}
