package rcloneconfig

import (
	"fmt"
	"strings"
)

// S3Remote defines parameters for constructing an rclone S3 remote.
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
func RenderS3(opts S3Remote) (string, error) {
	if opts.Name == "" {
		return "", fmt.Errorf("rcloneconfig: remote name is required")
	}
	if opts.Region == "" {
		return "", fmt.Errorf("rcloneconfig: region is required")
	}
	if opts.AccessKeyID == "" || opts.SecretAccessKey == "" {
		return "", fmt.Errorf("rcloneconfig: access key and secret are required")
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
func MustRenderS3(opts S3Remote) string {
	s, err := RenderS3(opts)
	if err != nil {
		panic(err)
	}
	return s
}
