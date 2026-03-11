package s3storage

import (
	"context"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/stretchr/testify/require"
)

func TestS3StorageWithFakeS3(t *testing.T) {
	fake := gofakes3.New(s3mem.New())
	server := fakeServer(t, fake)
	if server == nil {
		t.Fatalf("unable to start fake s3 server")
	}

	ensureBucket(t, server.URL, "bucket")

	fs, err := New(Config{
		Bucket:          "bucket",
		Endpoint:        server.URL,
		Region:          "us-east-1",
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		UsePathStyle:    true,
	})
	require.NoError(t, err)

	require.NoError(t, fs.Put("hello.txt", []byte("s3")))
	got, err := fs.Get("hello.txt")
	require.NoError(t, err)
	require.Equal(t, "s3", string(got))
}

func fakeServer(t *testing.T, fake *gofakes3.GoFakeS3) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for fake s3: %v", err)
	}
	ts := httptest.NewUnstartedServer(fake.Server())
	ts.Listener = ln
	ts.Start()
	return ts
}

func ensureBucket(t *testing.T, endpoint, bucket string) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
		config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
			func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: endpoint, HostnameImmutable: true}, nil
			})),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("access", "secret", "")),
	)
	if err != nil {
		t.Fatalf("fake s3 bucket setup failed: %v", err)
	}
	awsS3 := s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = true })
	_, err = awsS3.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("fake s3 bucket creation failed: %v", err)
	}
}
