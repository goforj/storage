package s3storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/goforj/storage/storagecore"
)

// TestObjectListingDecodesOpaqueKeysExactlyOnce prevents XML-sensitive keys from poisoning a provider page.
func TestObjectListingDecodesOpaqueKeysExactlyOnce(t *testing.T) {
	keys := []string{"tenant/control\x01key", "tenant/plus+percent%2F space", "tenant/雪"}
	malformed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("encoding-type") != "url" {
			t.Error("listing did not request URL encoding")
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, "<ListBucketResult><EncodingType>url</EncodingType><IsTruncated>false</IsTruncated>")
		for _, key := range keys {
			encoded := strings.ReplaceAll(url.QueryEscape(key), "+", "%20")
			if malformed {
				encoded = "%ZZ"
			}
			fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>1</Size></Contents>", encoded)
		}
		fmt.Fprint(w, "</ListBucketResult>")
	}))
	defer server.Close()
	disk, err := New(Config{Bucket: "fixture", Region: "us-east-1", Endpoint: server.URL, AccessKeyID: "fixture", SecretAccessKey: "fixture", UsePathStyle: true, Prefix: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	objects := disk.(storagecore.ObjectStorage)
	var found []string
	if err := objects.WalkObjectsContext(context.Background(), "", func(info storagecore.ObjectInfo) error { found = append(found, info.Key); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(found) != len(keys) {
		t.Fatal("objects omitted", found)
	}
	for i, key := range keys {
		if found[i] != strings.TrimPrefix(key, "tenant/") {
			t.Errorf("opaque key changed: got %q want %q", found[i], key)
		}
	}
	malformed = true
	called := false
	if err := objects.WalkObjectsContext(context.Background(), "", func(storagecore.ObjectInfo) error { called = true; return nil }); err == nil || called {
		t.Fatal("malformed encoding accepted", called, err)
	}
}

// TestNonseekableUploadsRespectHTTPPayloadSigning preserves signing policy without buffering entire objects.
func TestNonseekableUploadsRespectHTTPPayloadSigning(t *testing.T) {
	for _, mode := range []string{"explicit", "uppercase", "environment", "service-environment"} {
		for _, unsigned := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, "/unsigned=", unsigned), func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "payload" {
						t.Errorf("stream changed: %q, %v", body, err)
					}
					if r.Header.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" {
						t.Error("explicit unsigned policy not honored")
					}
					w.Header().Set("ETag", "\"fixture\"")
				}))
				defer server.Close()
				endpoint := server.URL
				switch mode {
				case "uppercase":
					endpoint = strings.Replace(endpoint, "http:", "HTTP:", 1)
				case "environment":
					t.Setenv("AWS_ENDPOINT_URL", endpoint)
					endpoint = ""
				case "service-environment":
					t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
					endpoint = ""
				}
				disk, err := New(Config{Bucket: "fixture", Region: "us-east-1", Endpoint: endpoint, AccessKeyID: "fixture", SecretAccessKey: "fixture", UsePathStyle: true, UnsignedPayload: unsigned})
				if err != nil {
					t.Fatal(err)
				}
				_, err = disk.(storagecore.ObjectStorage).PutObjectContext(context.Background(), "key", io.LimitReader(strings.NewReader("payload"), 7), storagecore.PutObjectOptions{Size: 7})
				if unsigned {
					if err != nil || requests != 1 {
						t.Fatal("opted-in streaming upload failed", requests, err)
					}
				} else if !errors.Is(err, storagecore.ErrUnsupported) || requests != 0 {
					t.Fatal("signed payload policy weakened", requests, err)
				}
			})
		}
	}

}

// TestNonseekableHTTPSUploadsRetainStreamingChecksums exercises the normal SDK streaming protocol over TLS.
func TestNonseekableHTTPSUploadsRetainStreamingChecksums(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), "payload") {
			t.Errorf("streaming payload lost: %q, %v", body, err)
		}
		if !strings.Contains(r.Header.Get("X-Amz-Trailer"), "x-amz-checksum-") || r.Header.Get("X-Amz-Content-Sha256") != "STREAMING-UNSIGNED-PAYLOAD-TRAILER" {
			t.Errorf("HTTPS checksum headers: algorithm=%q trailer=%q hash=%q encoding=%q", r.Header.Get("X-Amz-Sdk-Checksum-Algorithm"), r.Header.Get("X-Amz-Trailer"), r.Header.Get("X-Amz-Content-Sha256"), r.Header.Get("Content-Encoding"))
		}
		w.Header().Set("ETag", "\"fixture\"")
	}))
	defer server.Close()
	original := buildS3Clients
	t.Cleanup(func() { buildS3Clients = original })
	buildS3Clients = func(cfg aws.Config, resolved storagecore.ResolvedConfig) (s3API, s3PresignAPI) {
		cfg.HTTPClient = server.Client()
		return original(cfg, resolved)
	}
	disk, err := New(Config{Bucket: "fixture", Region: "us-east-1", Endpoint: server.URL, AccessKeyID: "fixture", SecretAccessKey: "fixture", UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = disk.(storagecore.ObjectStorage).PutObjectContext(context.Background(), "key", io.LimitReader(strings.NewReader("payload"), 7), storagecore.PutObjectOptions{Size: 7})
	if err != nil || requests != 1 {
		t.Fatal("TLS stream failed", requests, err)
	}
}

// cancelUploadReader records source consumption when cancellation arrives during SDK checksum computation.
type cancelUploadReader struct {
	reader *strings.Reader
	cancel context.CancelFunc
	bytes  int
	calls  int
}

// Read cancels the request after its first bounded read to expose uninterruptible prehash loops.
func (r *cancelUploadReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += n
	r.calls++
	if r.calls == 1 {
		r.cancel()
	}
	return n, err
}

// Seek retains the source's rewindability so the SDK takes its prehash path.
func (r *cancelUploadReader) Seek(offset int64, whence int) (int64, error) {
	return r.reader.Seek(offset, whence)
}

// TestUploadCancellationStopsPrehashConsumption verifies cancellation after hashing has started, before any network request.
func TestUploadCancellationStopsPrehashConsumption(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	disk, err := New(Config{Bucket: "fixture", Region: "us-east-1", Endpoint: server.URL, AccessKeyID: "fixture", SecretAccessKey: "fixture", UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &cancelUploadReader{reader: strings.NewReader(strings.Repeat("x", 4<<20)), cancel: cancel}
	_, err = disk.(storagecore.ObjectStorage).PutObjectContext(ctx, "key", body, storagecore.PutObjectOptions{Size: 4 << 20})
	if !errors.Is(err, context.Canceled) || body.calls != 1 || body.bytes >= 4<<20 || requests != 0 {
		t.Fatal("cancellation consumed the remaining source", body.calls, body.bytes, requests, err)
	}
}

// TestCanceledObjectBodyDoesNotTouchSource ensures canceled SDK reads and rewinds leave caller state untouched.
func TestCanceledObjectBodyDoesNotTouchSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := strings.NewReader("payload")
	body := contextObjectBody(ctx, source)
	cancel()
	if n, err := body.Read(make([]byte, 3)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %d, %v", n, err)
	}
	if _, err := body.(io.Seeker).Seek(2, io.SeekStart); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled seek = %v", err)
	}
	if position, err := source.Seek(0, io.SeekCurrent); err != nil || position != 0 {
		t.Fatalf("canceled operation moved source: %d, %v", position, err)
	}
}
