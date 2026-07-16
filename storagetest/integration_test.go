package storagetest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// TestGetenvDefault verifies that non-empty environment values override fixture defaults.
func TestGetenvDefault(t *testing.T) {
	const key = "STORAGETEST_GETENV_DEFAULT"
	t.Setenv(key, "")
	if got := GetenvDefault(key, "fallback"); got != "fallback" {
		t.Fatalf("GetenvDefault fallback = %q", got)
	}

	t.Setenv(key, "set")
	if got := GetenvDefault(key, "fallback"); got != "set" {
		t.Fatalf("GetenvDefault set = %q", got)
	}
}

// TestRequireIntegrationWhenEnabled verifies that the integration gate permits opted-in tests.
func TestRequireIntegrationWhenEnabled(t *testing.T) {
	t.Setenv("RUN_INTEGRATION", "1")
	RequireIntegration(t)
}

// TestRequireIntegrationSkipsWhenDisabled verifies that integration helpers skip without explicit opt-in.
func TestRequireIntegrationSkipsWhenDisabled(t *testing.T) {
	t.Setenv("RUN_INTEGRATION", "")
	t.Run("skip", func(t *testing.T) {
		RequireIntegration(t)
	})
}

// TestS3Settings verifies that every S3 fixture setting honors its environment override.
func TestS3Settings(t *testing.T) {
	t.Setenv("INTEGRATION_S3_ENDPOINT", "http://example")
	t.Setenv("INTEGRATION_S3_REGION", "region")
	t.Setenv("INTEGRATION_S3_ACCESS_KEY", "access")
	t.Setenv("INTEGRATION_S3_SECRET_KEY", "secret")
	t.Setenv("INTEGRATION_S3_BUCKET", "bucket")

	endpoint, region, access, secret, bucket := S3Settings()
	if endpoint != "http://example" || region != "region" || access != "access" || secret != "secret" || bucket != "bucket" {
		t.Fatalf("S3Settings = %q %q %q %q %q", endpoint, region, access, secret, bucket)
	}
}

// TestEnsureS3BucketCanceled verifies that S3 fixture setup respects caller cancellation.
func TestEnsureS3BucketCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := EnsureS3Bucket(ctx, "http://127.0.0.1:1", "us-east-1", "access", "secret", "bucket"); err == nil {
		t.Fatal("EnsureS3Bucket returned nil error for canceled context")
	}
}

// TestEnsureS3BucketSuccess verifies that S3 bucket setup is idempotent against an emulator.
func TestEnsureS3BucketSuccess(t *testing.T) {
	serverURL := fakeS3ServerURL(t)

	if err := EnsureS3Bucket(context.Background(), serverURL, "us-east-1", "access", "secret", "bucket"); err != nil {
		t.Fatalf("EnsureS3Bucket create: %v", err)
	}
	if err := EnsureS3Bucket(context.Background(), serverURL, "us-east-1", "access", "secret", "bucket"); err != nil {
		t.Fatalf("EnsureS3Bucket existing: %v", err)
	}
}

// TestGCSSettings verifies that GCS fixture endpoint and bucket overrides are returned together.
func TestGCSSettings(t *testing.T) {
	t.Setenv("INTEGRATION_GCS_ENDPOINT", "http://example")
	t.Setenv("INTEGRATION_GCS_BUCKET", "bucket")

	endpoint, bucket := GCSSettings()
	if endpoint != "http://example" || bucket != "bucket" {
		t.Fatalf("GCSSettings = %q %q", endpoint, bucket)
	}
}

// TestReachable distinguishes an accepting listener from a closed loopback port.
func TestReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if !Reachable(ln.Addr().String()) {
		t.Fatalf("Reachable(%q) = false", ln.Addr().String())
	}

	if Reachable("127.0.0.1:1") {
		t.Fatal("Reachable should be false for closed port")
	}
}

// TestEnsureGCSBucketCanceled verifies cancellation while retaining the emulator host convention.
func TestEnsureGCSBucketCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := EnsureGCSBucket(ctx, "http://127.0.0.1:1", "bucket")
	if err == nil {
		t.Fatal("EnsureGCSBucket returned nil error for canceled context")
	}
	if got := os.Getenv("STORAGE_EMULATOR_HOST"); got != "127.0.0.1:1" {
		t.Fatalf("STORAGE_EMULATOR_HOST = %q", got)
	}
}

// TestEnsureGCSBucketSuccess verifies that GCS bucket setup is idempotent against an emulator.
func TestEnsureGCSBucketSuccess(t *testing.T) {
	host := "127.0.0.1"
	port := uint16(pickPort(t))
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme:     "http",
		Host:       host,
		Port:       port,
		PublicHost: fmt.Sprintf("%s:%d", host, port),
	})
	if err != nil {
		t.Fatalf("start fake gcs server: %v", err)
	}
	defer server.Stop()

	if err := EnsureGCSBucket(context.Background(), server.URL(), "bucket"); err != nil {
		t.Fatalf("EnsureGCSBucket create: %v", err)
	}
	if err := EnsureGCSBucket(context.Background(), server.URL(), "bucket"); err != nil {
		t.Fatalf("EnsureGCSBucket existing: %v", err)
	}
}

// pickPort reserves an ephemeral loopback port long enough to discover its assigned number.
func pickPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pickPort: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// fakeS3ServerURL starts an in-memory S3-compatible HTTP server with test-scoped cleanup.
func fakeS3ServerURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake s3: %v", err)
	}

	server := &http.Server{Handler: gofakes3.New(s3mem.New()).Server()}
	go func() {
		_ = server.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = ln.Close()
	})

	return "http://" + ln.Addr().String()
}
