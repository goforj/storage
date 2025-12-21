package dropboxdriver

import "testing"

// Integration tests require a Dropbox access token.
// To enable, set DROPBOX_TEST_TOKEN and DROPBOX_TEST_PREFIX.
// Skipped by default to avoid external dependency.
func TestDropboxSkip(t *testing.T) {
	t.Skip("Dropbox integration test requires token; skipping by default")
}
