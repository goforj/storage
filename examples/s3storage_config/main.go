//go:build ignore
// +build ignore

package main

import "github.com/goforj/storage/driver/s3storage"

func main() {
	// Config defines an S3-backed storage disk.

	// Example: define s3 storage config
	cfg := s3storage.Config{
		Bucket: "uploads",
		Region: "us-east-1",
	}
	_ = cfg

	// Example: define s3 storage config with all fields
	cfg := s3storage.Config{
		Bucket:          "uploads",
		Endpoint:        "http://localhost:9000", // default: ""
		Region:          "us-east-1",
		AccessKeyID:     "minioadmin", // default: ""
		SecretAccessKey: "minioadmin", // default: ""
		UsePathStyle:    true,         // default: false
		UnsignedPayload: false,        // default: false
		Prefix:          "assets",     // default: ""
	}
	_ = cfg
}
