package dropboxstorage

import "testing"

// TestDropboxSkip documents that live coverage requires DROPBOX_TEST_TOKEN and DROPBOX_TEST_PREFIX.
func TestDropboxSkip(t *testing.T) {
	t.Skip("Dropbox integration test requires token; skipping by default")
}
