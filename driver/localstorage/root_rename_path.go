//go:build (js && wasm) || plan9

package localstorage

import (
	"os"
	"path/filepath"
)

// renameAt matches os.Root's path-based fallback on platforms that cannot keep
// directory handles across renames and therefore already document TOCTOU limits.
func renameAt(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	return os.Rename(filepath.Join(oldParent.Name(), oldName), filepath.Join(newParent.Name(), newName))
}
