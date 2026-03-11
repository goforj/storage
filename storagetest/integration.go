package storagetest

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"google.golang.org/api/option"
)

const (
	defaultS3Endpoint = "http://localhost:9000"
	defaultS3Region   = "us-east-1"
	defaultS3Access   = "fsuser"
	defaultS3Secret   = "fspass123"
	defaultS3Bucket   = "fsintegration"

	defaultGCSEndpoint = "http://127.0.0.1:4443"
	defaultGCSBucket   = "gcs-integration"
)

// RequireIntegration skips unless RUN_INTEGRATION=1 is set.
func RequireIntegration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION") == "" {
		t.Skip("set RUN_INTEGRATION=1 to run integration tests")
	}
}

// S3Settings returns S3 config populated from env with integration defaults.
func S3Settings() (endpoint, region, access, secret, bucket string) {
	endpoint = getenvDefault("INTEGRATION_S3_ENDPOINT", defaultS3Endpoint)
	region = getenvDefault("INTEGRATION_S3_REGION", defaultS3Region)
	access = getenvDefault("INTEGRATION_S3_ACCESS_KEY", defaultS3Access)
	secret = getenvDefault("INTEGRATION_S3_SECRET_KEY", defaultS3Secret)
	bucket = getenvDefault("INTEGRATION_S3_BUCKET", defaultS3Bucket)
	return
}

// EnsureS3Bucket creates the bucket if it does not exist.
func EnsureS3Bucket(ctx context.Context, endpoint, region, access, secret, bucket string) error {
	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(access, secret, "")),
		config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
			func(service, r string, _ ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: endpoint, HostnameImmutable: true}, nil
			},
		)),
	)
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	var ae *types.BucketAlreadyOwnedByYou
	if err != nil && !errors.As(err, &ae) && !strings.Contains(strings.ToLower(err.Error()), "exists") {
		return err
	}
	return nil
}

// GCSSettings returns GCS config populated from env with integration defaults.
func GCSSettings() (endpoint, bucket string) {
	return getenvDefault("INTEGRATION_GCS_ENDPOINT", defaultGCSEndpoint),
		getenvDefault("INTEGRATION_GCS_BUCKET", defaultGCSBucket)
}

// EnsureGCSBucket creates the bucket if it does not exist.
func EnsureGCSBucket(ctx context.Context, endpoint, bucket string) error {
	_ = os.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(endpoint, "http://"))

	client, err := storage.NewClient(ctx,
		option.WithEndpoint(endpoint),
		option.WithoutAuthentication(),
		option.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}),
	)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Bucket(bucket).Create(ctx, "test-project", nil); err != nil {
		if strings.Contains(err.Error(), "409") {
			return nil
		}
		w := client.Bucket(bucket).Object("healthcheck.txt").NewWriter(ctx)
		if _, werr := w.Write([]byte("ok")); werr == nil {
			_ = w.Close()
			return nil
		}
		return err
	}
	return nil
}

// Reachable checks TCP reachability to host:port.
func Reachable(addr string) bool {
	d := net.Dialer{Timeout: 500 * time.Millisecond, KeepAlive: 0}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// GetenvDefault is exported for tests that need defaulted env reads.
func GetenvDefault(key, def string) string {
	return getenvDefault(key, def)
}
