package rcloneconfig

import (
	"fmt"
)

// ExampleRenderLocal shows generating a local remote section.
func ExampleRenderLocal() {
	cfg, err := RenderLocal(LocalRemote{Name: "localdata"})
	fmt.Println(err == nil)
	fmt.Println(cfg)
	// Output:
	// true
	// [localdata]
	// type = local
}

// ExampleRenderS3 shows generating an S3 remote section.
func ExampleRenderS3() {
	cfg, err := RenderS3(S3Remote{
		Name:            "s3primary",
		Region:          "us-east-1",
		AccessKeyID:     "AKIA...",
		SecretAccessKey: "SECRET",
		Endpoint:        "http://localhost:4566",
		PathStyle:       true,
	})
	fmt.Println(err == nil)
	fmt.Println(cfg)
	// Output:
	// true
	// [s3primary]
	// type = s3
	// provider = AWS
	// access_key_id = AKIA...
	// secret_access_key = SECRET
	// region = us-east-1
	// endpoint = http://localhost:4566
	// force_path_style = true
}
