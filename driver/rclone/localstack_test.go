package rclone

import (
	"context"
	"errors"
	filesystemtest "github.com/goforj/filesystem/testutil"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/goforj/filesystem"
	"github.com/goforj/filesystem/rcloneconfig"
)

// Optional integration against localstack S3.
// Set LOCALSTACK_S3_ENDPOINT (e.g., http://localhost:4566) to enable.
func TestRcloneWithLocalstackS3(t *testing.T) {
	if os.Getenv("RUN_LOCALSTACK_S3") == "" {
		t.Skip("RUN_LOCALSTACK_S3 not set; skipping localstack integration test")
	}

	endpoint := os.Getenv("LOCALSTACK_S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}

	if !isEndpointReachable(endpoint) {
		t.Skipf("localstack endpoint %s not reachable; skipping", endpoint)
	}

	bucket := "filesystemtest"
	awsCfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("access", "secret", ""),
		EndpointResolverWithOptions: aws.EndpointResolverWithOptionsFunc(
			func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               endpoint,
					HostnameImmutable: true,
				}, nil
			}),
	}
	awsS3 := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	ctx := context.Background()

	if _, err := awsS3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
		// If already exists, proceed.
		var apiErr *s3types.BucketAlreadyOwnedByYou
		if !errors.As(err, &apiErr) {
			t.Skipf("create bucket failed (likely no localstack or path-style mismatch): %v", err)
		}
	}
	// No cleanup to allow inspection in localstack; disable if you need isolated runs.

	conf := rcloneconfig.MustRenderS3(rcloneconfig.S3Remote{
		Name:               "s3stack",
		Endpoint:           endpoint,
		Region:             "us-east-1",
		Provider:           "Minio",
		AccessKeyID:        "access",
		SecretAccessKey:    "secret",
		PathStyle:          true,
		UseUnsignedPayload: true,
	})

	cfg := filesystem.Config{
		Default:          "s3",
		RcloneConfigData: conf,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"s3": {
				Driver: "rclone",
				Remote: "s3stack:" + bucket,
				Prefix: "sandbox",
			},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Skipf("New manager failed (likely due to existing rclone init or endpoint issues): %v", err)
	}
	fsys, err := mgr.Disk("s3")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	err = fsys.Put(ctx, "foo2.txt", []byte("hello localstack"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	filesystemtest.RunFilesystemContractTests(t, fsys)
}

func isEndpointReachable(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}
