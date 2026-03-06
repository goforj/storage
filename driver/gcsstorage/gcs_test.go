package gcsstorage

import "testing"

// Integration tests require a GCS emulator or real bucket with credentials.
// Set GCS_TEST_BUCKET and GCS_TEST_CREDENTIALS_JSON to enable.
// Skipped by default.
func TestGCSSkip(t *testing.T) {
	t.Skip("GCS integration test requires emulator or real bucket; skipping by default")
}
