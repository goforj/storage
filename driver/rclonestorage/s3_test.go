package rclonestorage

import (
	"strings"
	"testing"
)

// TestRenderS3Validation verifies remote names, regions, and credential pairs are required.
func TestRenderS3Validation(t *testing.T) {
	_, err := RenderS3(S3Remote{})
	if err == nil {
		t.Fatalf("expected error for missing name")
	}
	_, err = RenderS3(S3Remote{Name: "ok"})
	if err == nil {
		t.Fatalf("expected error for missing region and creds")
	}
	_, err = RenderS3(S3Remote{Name: "ok", Region: "us", AccessKeyID: "id"})
	if err == nil {
		t.Fatalf("expected error for missing secret")
	}
}

// TestRenderS3Output verifies defaults and optional S3 directives render canonically.
func TestRenderS3Output(t *testing.T) {
	out, err := RenderS3(S3Remote{
		Name:               "remote",
		Region:             "us-east-1",
		AccessKeyID:        "id",
		SecretAccessKey:    "secret",
		PathStyle:          true,
		Provider:           "",
		BucketACL:          "private",
		UseUnsignedPayload: true,
		Endpoint:           "http://localhost",
	})
	if err != nil {
		t.Fatalf("RenderS3: %v", err)
	}
	if !contains(out, "type = s3") || !contains(out, "provider = AWS") {
		t.Fatalf("expected defaults present in output: %q", out)
	}
	if !contains(out, "force_path_style = true") {
		t.Fatalf("expected path style flag in output")
	}
	if !contains(out, "endpoint = http://localhost") {
		t.Fatalf("expected endpoint in output")
	}
	if !contains(out, "acl = private") {
		t.Fatalf("expected acl in output")
	}
	if !contains(out, "use_unsigned_payload = true") {
		t.Fatalf("expected unsigned payload flag")
	}
}

// TestMustRenderS3Panics verifies invalid S3 configuration triggers the convenience panic.
func TestMustRenderS3Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = MustRenderS3(S3Remote{})
}

// contains matches a complete rendered configuration line.
func contains(body, needle string) bool {
	return strings.Contains(body, needle)
}
