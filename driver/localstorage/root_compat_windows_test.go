//go:build windows

package localstorage

import (
	"errors"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// TestValidateRenameNameRejectsWindowsSpecialPaths verifies the direct leaf
// operation cannot address drive-relative, stream, or reserved device names.
func TestValidateRenameNameRejectsWindowsSpecialPaths(t *testing.T) {
	for _, name := range []string{"C:relative", "file:stream", "file?name", "NUL", "dir/CON"} {
		if err := validateRenameName(name); !errors.Is(err, storagecore.ErrForbidden) {
			t.Errorf("validateRenameName(%q) error = %v", name, err)
		}
	}
}
