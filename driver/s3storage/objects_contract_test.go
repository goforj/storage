package s3storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/goforj/storage/storagecore"
)

// bucketFixture records provisioning separately from ordinary object operations.
type bucketFixture struct {
	fakeS3
	inspectErr error
	createErr  error
	created    []*s3.CreateBucketInput
}

// HeadBucket supplies provider failure classes without network retry delays.
func (f *bucketFixture) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, f.inspectErr
}

// CreateBucket records the region constraint and preserves provisioning errors.
func (f *bucketFixture) CreateBucket(_ context.Context, input *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.created = append(f.created, input)
	return &s3.CreateBucketOutput{}, f.createErr
}

// TestBucketPreparationFailureBoundaries ensures only missing buckets trigger provisioning.
func TestBucketPreparationFailureBoundaries(t *testing.T) {
	original := buildS3Clients
	t.Cleanup(func() { buildS3Clients = original })
	cfg := Config{Bucket: "fixture", Region: "eu-west-1", AccessKeyID: "fixture", SecretAccessKey: "fixture"}
	for _, test := range []struct {
		name        string
		client      s3API
		cfg         Config
		wantError   bool
		wantCreates int
	}{
		{name: "invalid configuration", client: &bucketFixture{}, cfg: Config{}, wantError: true},
		{name: "unsupported provisioning", client: &fakeS3{}, cfg: cfg, wantError: true},
		{name: "transport failure", client: &bucketFixture{inspectErr: errors.New("transport failed")}, cfg: cfg, wantError: true},
		{name: "missing regional bucket", client: &bucketFixture{inspectErr: &smithy.GenericAPIError{Code: "NoSuchBucket"}}, cfg: cfg, wantCreates: 1},
		{name: "create failure", client: &bucketFixture{inspectErr: &smithy.GenericAPIError{Code: "NotFound"}, createErr: errors.New("creation refused")}, cfg: cfg, wantError: true, wantCreates: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			buildS3Clients = func(aws.Config, storagecore.ResolvedConfig) (s3API, s3PresignAPI) { return test.client, nil }
			err := PrepareBucketContext(context.Background(), test.cfg)
			if (err != nil) != test.wantError {
				t.Fatal("unexpected provisioning outcome", err)
			}
			if fixture, ok := test.client.(*bucketFixture); ok {
				if len(fixture.created) != test.wantCreates {
					t.Fatal("unexpected creation attempt", fixture.created)
				}
				for _, created := range fixture.created {
					if created.CreateBucketConfiguration == nil || string(created.CreateBucketConfiguration.LocationConstraint) != "eu-west-1" {
						t.Fatal("regional constraint lost", created)
					}
				}
			}
		})
	}
}

// objectResponseFixture supplies streaming response ownership and metadata independently of list fixtures.
type objectResponseFixture struct {
	fakeS3
	response *s3.GetObjectOutput
}

// GetObject records conditional inputs while returning the configured streaming response.
func (f *objectResponseFixture) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getInputs = append(f.getInputs, input)
	return f.response, nil
}

// TestObjectPaginationPreservesContinuationAndCancellation checks page boundaries and callback stop behavior.
func TestObjectPaginationPreservesContinuationAndCancellation(t *testing.T) {
	modified := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	fake := &fakeS3{listSeq: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{{Key: aws.String("first"), Size: aws.Int64(1), LastModified: &modified}}, IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("second-page")},
		{Contents: []types.Object{{Key: aws.String("second"), Size: aws.Int64(2)}}},
	}}
	disk := &driver{client: fake, bucket: "fixture"}
	var found []storagecore.ObjectInfo
	if err := disk.WalkObjectsContext(context.Background(), "", func(info storagecore.ObjectInfo) error { found = append(found, info); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Key != "first" || !found[0].ModifiedAt.Equal(modified) || found[1].Key != "second" || len(fake.listInputs) != 2 || aws.ToString(fake.listInputs[1].ContinuationToken) != "second-page" {
		t.Fatal("pagination lost data", found, fake.listInputs)
	}
	fake.listSeq = []*s3.ListObjectsV2Output{{IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("repeated")}, {IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("repeated")}}
	if err := disk.WalkObjectsContext(context.Background(), "", func(storagecore.ObjectInfo) error { return nil }); err == nil {
		t.Fatal("repeated token accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	fake.listHook = cancel
	fake.listOut = &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("after-cancellation")}}}
	called := false
	if err := disk.WalkObjectsContext(ctx, "", func(storagecore.ObjectInfo) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled page delivered", called, err)
	}
}

// TestObjectStreamingOwnershipAndMetadata verifies stream lifetime and metadata at the provider boundary.
func TestObjectStreamingOwnershipAndMetadata(t *testing.T) {
	modified := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	body := &trackedReadCloser{reader: strings.NewReader("data")}
	fixture := &objectResponseFixture{response: &s3.GetObjectOutput{Body: body, ContentLength: aws.Int64(4), ETag: aws.String("\"observed\""), VersionId: aws.String("version"), LastModified: &modified, ContentType: aws.String("text/plain"), ContentEncoding: aws.String("identity"), ContentDisposition: aws.String("attachment"), CacheControl: aws.String("max-age=60"), Metadata: map[string]string{"owner": "fixture"}}}
	disk := &driver{client: fixture, bucket: "fixture"}
	reader, err := disk.OpenObjectContext(context.Background(), "path/../opaque", storagecore.OpenObjectOptions{Version: "version", IfMatch: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	if body.closed {
		t.Fatal("stream closed before caller finished")
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "data" || reader.Info.Version != "version" || !reader.Info.ModifiedAt.Equal(modified) {
		t.Fatal("stream metadata lost", reader.Info, err)
	}
	if err := reader.Close(); err != nil || !body.closed {
		t.Fatal("caller close lost", err)
	}
	metadata := reader.Info.Metadata
	if metadata.ContentType != "text/plain" || metadata.ContentEncoding != "identity" || metadata.ContentDisposition != "attachment" || metadata.CacheControl != "max-age=60" || metadata.Values["owner"] != "fixture" {
		t.Fatal("read metadata lost", metadata)
	}
	if _, err := disk.PutObjectContext(context.Background(), "path/../opaque", strings.NewReader("data"), storagecore.PutObjectOptions{Size: 4, Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	put := fixture.putInputs[0]
	if aws.ToString(put.Key) != "path/../opaque" || aws.ToString(put.ContentEncoding) != "identity" || aws.ToString(put.ContentDisposition) != "attachment" || aws.ToString(put.CacheControl) != "max-age=60" || put.Metadata["owner"] != "fixture" {
		t.Fatal("write metadata or key changed", put)
	}
	body.closed = false
	if _, err := disk.OpenObjectContext(context.Background(), "key", storagecore.OpenObjectOptions{IfMatch: "stale"}); err == nil || !body.closed {
		t.Fatal("stale stream retained")
	}
}
