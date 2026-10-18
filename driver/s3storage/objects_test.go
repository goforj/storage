package s3storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/goforj/storage/storagecore"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// TestStreamingObjectsPreserveOpaqueKeysAndMetadata exercises the optional capability against the provider protocol.
func TestStreamingObjectsPreserveOpaqueKeysAndMetadata(t *testing.T) {
	server := fakeServer(t, gofakes3.New(s3mem.New()))
	defer server.Close()
	cfg := Config{Bucket: "streaming", Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "access", SecretAccessKey: "secret", UsePathStyle: true, Prefix: "tenant"}
	if err := PrepareBucketContext(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	store, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	objects := store.(storagecore.ObjectStorage)
	ctx := context.Background()
	key := "path/../odd//name"
	info, err := objects.PutObjectContext(ctx, key, strings.NewReader("payload"), storagecore.PutObjectOptions{Size: 7, Metadata: storagecore.ObjectMetadata{ContentType: "text/plain", Values: map[string]string{"owner": "fixture"}}})
	if err != nil || info.Size != 7 {
		t.Fatal(info, err)
	}
	count := 0
	if err := objects.WalkObjectsContext(ctx, "", func(info storagecore.ObjectInfo) error {
		count++
		if info.Key != key || info.Size != 7 {
			t.Errorf("key or size changed: %#v", info)
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	reader, err := objects.OpenObjectContext(ctx, key, storagecore.OpenObjectOptions{IfMatch: info.ETag})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(data) != "payload" || reader.Info.Metadata.ContentType != "text/plain" || reader.Info.Metadata.Values["owner"] != "fixture" {
		t.Fatal("stream or metadata changed", err, reader.Info)
	}
	if _, err := objects.OpenObjectContext(ctx, key, storagecore.OpenObjectOptions{IfMatch: "different"}); err == nil {
		t.Fatal("conditional read accepted changed object")
	}
	stop := errors.New("stop")
	if err := objects.WalkObjectsContext(ctx, "", func(storagecore.ObjectInfo) error { return stop }); !errors.Is(err, stop) {
		t.Fatal("callback failure lost", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := objects.WalkObjectsContext(canceled, "", func(storagecore.ObjectInfo) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := objects.OpenObjectContext(canceled, key, storagecore.OpenObjectOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := objects.PutObjectContext(canceled, key, strings.NewReader(""), storagecore.PutObjectOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, bad := range []string{"bad\x00key"} {
		if _, err := objects.OpenObjectContext(ctx, bad, storagecore.OpenObjectOptions{}); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatal(err)
		}
		if _, err := objects.PutObjectContext(ctx, bad, strings.NewReader(""), storagecore.PutObjectOptions{}); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatal(err)
		}
		if err := objects.WalkObjectsContext(ctx, bad, func(storagecore.ObjectInfo) error { return nil }); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatal(err)
		}
	}
	if err := objects.WalkObjectsContext(ctx, "", nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
	if _, err := objects.PutObjectContext(ctx, key, nil, storagecore.PutObjectOptions{}); err == nil {
		t.Fatal("nil body accepted")
	}
	if _, err := objects.PutObjectContext(ctx, key, strings.NewReader(""), storagecore.PutObjectOptions{Size: -1}); err == nil {
		t.Fatal("negative size accepted")
	}
}

// TestStreamingObjectFailuresKeepScopeAndStopPagination validates provider and pagination failures without buffering.
func TestStreamingObjectFailuresKeepScopeAndStopPagination(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("provider failed")
	for _, op := range []string{"list", "read", "write"} {
		fake := &fakeS3{listErr: failure, getErr: failure, putErr: failure}
		disk := &driver{client: fake, prefix: "tenant", bucket: "fixture"}
		var err error
		switch op {
		case "list":
			err = disk.WalkObjectsContext(ctx, "", func(storagecore.ObjectInfo) error { return nil })
		case "read":
			_, err = disk.OpenObjectContext(ctx, "key", storagecore.OpenObjectOptions{Version: "pinned-version"})
			if len(fake.getInputs) != 1 || *fake.getInputs[0].VersionId != "pinned-version" {
				t.Fatal("version pin lost")
			}
		case "write":
			_, err = disk.PutObjectContext(ctx, "key", strings.NewReader("data"), storagecore.PutObjectOptions{Size: 4})
		}
		if err == nil {
			t.Fatal("provider error swallowed", op)
		}
	}
	for _, page := range []*s3.ListObjectsV2Output{
		{IsTruncated: aws.Bool(true)},
		{Contents: []types.Object{{Key: aws.String("tenant-other/key")}}},
	} {
		disk := &driver{client: &fakeS3{listOut: page}, prefix: "tenant", bucket: "fixture"}
		if err := disk.WalkObjectsContext(ctx, "", func(storagecore.ObjectInfo) error { return nil }); err == nil {
			t.Fatal("invalid listing accepted")
		}
	}
	disk := &driver{client: &fakeS3{}, bucket: "fixture"}
	if _, err := disk.OpenObjectContext(ctx, "", storagecore.OpenObjectOptions{}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatal("empty root key accepted", err)
	}
}

// TestPrepareBucketDistinguishesForbiddenAndMissing verifies provisioning stays opt-in and permission failures are preserved.
func TestPrepareBucketDistinguishesForbiddenAndMissing(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			created := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(status)
					return
				}
				if r.Method == http.MethodPut {
					created++
					w.WriteHeader(200)
					return
				}
				w.WriteHeader(500)
			}))
			defer server.Close()
			err := PrepareBucketContext(context.Background(), Config{Bucket: "fixture", Region: "us-east-1", Endpoint: server.URL, AccessKeyID: "fixture", SecretAccessKey: "fixture", UsePathStyle: true})
			if status == 403 {
				if err == nil || created != 0 {
					t.Fatal("forbidden bucket provisioned", err, created)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if status == 404 && created != 1 {
				t.Fatal("missing bucket not prepared", created)
			}
		})
	}
}
