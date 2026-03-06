package rclonestorage

import (
	"fmt"
	"strings"
)

// S3Remote defines parameters for constructing an rclone S3 remote.
// @group Config
//
// Example: define an s3 remote
//
//	remote := rclonestorage.S3Remote{
//		Name:            "assets",
//		Region:          "us-east-1",
//		AccessKeyID:     "key",
//		SecretAccessKey: "secret",
//	}
//	fmt.Println(remote.Name)
//	// Output: assets
//
// Example: define an s3 remote with all fields
//
//	remote := rclonestorage.S3Remote{
//		Name:               "assets",
//		Endpoint:           "http://localhost:9000", // default: ""
//		Region:             "us-east-1",
//		AccessKeyID:        "key",
//		SecretAccessKey:    "secret",
//		Provider:           "AWS",    // default: "AWS"
//		PathStyle:          false,    // default: false
//		BucketACL:          "private", // default: ""
//		UseUnsignedPayload: false,    // default: false
//	}
//	fmt.Println(remote.Name)
//	// Output: assets
type S3Remote struct {
	Name               string
	Endpoint           string
	Region             string
	AccessKeyID        string
	SecretAccessKey    string
	Provider           string // optional, defaults to "AWS"
	PathStyle          bool
	BucketACL          string // optional
	UseUnsignedPayload bool   // optional
}

// RenderS3 returns ini-formatted rclone config content for a single S3 remote.
// @group Config
//
// Example: render an s3 remote
//
//	cfg, _ := rclonestorage.RenderS3(rclonestorage.S3Remote{
//		Name:            "assets",
//		Region:          "us-east-1",
//		AccessKeyID:     "key",
//		SecretAccessKey: "secret",
//	})
//	fmt.Println(cfg)
//	// Output:
//	// [assets]
//	// type = s3
//	// provider = AWS
//	// access_key_id = key
//	// secret_access_key = secret
//	// region = us-east-1
func RenderS3(opts S3Remote) (string, error) {
	if opts.Name == "" {
		return "", fmt.Errorf("rclone: remote name is required")
	}
	if opts.Region == "" {
		return "", fmt.Errorf("rclone: region is required")
	}
	if opts.AccessKeyID == "" || opts.SecretAccessKey == "" {
		return "", fmt.Errorf("rclone: access key and secret are required")
	}
	provider := opts.Provider
	if provider == "" {
		provider = "AWS"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", opts.Name)
	fmt.Fprintf(&b, "type = s3\n")
	fmt.Fprintf(&b, "provider = %s\n", provider)
	fmt.Fprintf(&b, "access_key_id = %s\n", opts.AccessKeyID)
	fmt.Fprintf(&b, "secret_access_key = %s\n", opts.SecretAccessKey)
	fmt.Fprintf(&b, "region = %s\n", opts.Region)
	if opts.Endpoint != "" {
		fmt.Fprintf(&b, "endpoint = %s\n", opts.Endpoint)
	}
	if opts.PathStyle {
		// rclone flag name uses "force_path_style"
		fmt.Fprintf(&b, "force_path_style = true\n")
	}
	if opts.BucketACL != "" {
		fmt.Fprintf(&b, "acl = %s\n", opts.BucketACL)
	}
	if opts.UseUnsignedPayload {
		fmt.Fprintf(&b, "use_unsigned_payload = true\n")
	}

	return b.String(), nil
}

// MustRenderS3 panics on error.
// @group Config
//
// Example: render an s3 remote without handling the error
//
//	cfg := rclonestorage.MustRenderS3(rclonestorage.S3Remote{
//		Name:            "assets",
//		Region:          "us-east-1",
//		AccessKeyID:     "key",
//		SecretAccessKey: "secret",
//	})
//	fmt.Println(cfg)
//	// Output:
//	// [assets]
//	// type = s3
//	// provider = AWS
//	// access_key_id = key
//	// secret_access_key = secret
//	// region = us-east-1
func MustRenderS3(opts S3Remote) string {
	s, err := RenderS3(opts)
	if err != nil {
		panic(err)
	}
	return s
}
