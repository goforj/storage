package s3storage

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/stretchr/testify/require"

	"github.com/goforj/storage/storagecore"
)

// TestS3StorageWithFakeS3 exercises the public driver contract against an HTTP-compatible S3 server.
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

	require.NoError(t, fs.MakeDir("empty"))
	entry, err := fs.Stat("empty")
	require.NoError(t, err)
	require.True(t, entry.IsDir)
	require.NoError(t, fs.Delete("empty"))
	_, err = fs.Stat("empty")
	require.ErrorIs(t, err, storagecore.ErrNotFound)

	require.NoError(t, fs.MakeDir("nonempty"))
	require.NoError(t, fs.Put("nonempty/file.txt", []byte("child")))
	err = fs.Delete("nonempty")
	require.ErrorIs(t, err, storagecore.ErrForbidden)
}

// fakeServer binds IPv4 explicitly so endpoint tests do not depend on host IPv6 availability.
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

// ensureBucket creates the fixture through the same path-style endpoint configuration used by the driver.
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

// TestS3CopyPreservesMetadata verifies provider-native copy keeps content attributes and user metadata.
func TestS3CopyPreservesMetadata(t *testing.T) {
	ctx := context.Background()
	server := fakeServer(t, gofakes3.New(s3mem.New()))
	defer server.Close()
	ensureBucket(t, server.URL, "bucket")
	disk, err := New(Config{
		Bucket: "bucket", Region: "us-east-1", Endpoint: server.URL,
		AccessKeyID: "access", SecretAccessKey: "secret", UsePathStyle: true,
	})
	require.NoError(t, err)
	objects, err := storagecore.Objects(disk)
	require.NoError(t, err)
	_, err = objects.PutObjectContext(ctx, "nested/source +name", strings.NewReader("hello"), storagecore.PutObjectOptions{
		Size:     5,
		Metadata: storagecore.ObjectMetadata{ContentType: "text/plain", Values: map[string]string{"owner": "fixture"}},
	})
	require.NoError(t, err)
	require.NoError(t, disk.Copy("nested/source +name", "copy"))
	reader, err := objects.OpenObjectContext(ctx, "copy", storagecore.OpenObjectOptions{})
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, "text/plain", reader.Info.Metadata.ContentType)
	require.Equal(t, "fixture", reader.Info.Metadata.Values["owner"])
}
