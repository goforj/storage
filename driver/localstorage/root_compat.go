package localstorage

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/goforj/storage/storagecore"
)

// rootedMkdirAll uses Root for every component so each platform retains its
// documented rooted path guarantees while missing parents are created.
func rootedMkdirAll(root *os.Root, name string, perm fs.FileMode) error {
	if name == "." || name == "" {
		return nil
	}
	if err := validateRootedPath(name); err != nil {
		return err
	}
	current := ""
	for _, component := range strings.Split(name, "/") {
		current = path.Join(current, component)
		if err := root.Mkdir(current, perm); err != nil {
			if !os.IsExist(err) {
				return err
			}
			info, statErr := root.Stat(current)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return err
			}
		}
	}
	return nil
}

// rootedRename resolves both parents through Root before invoking the
// platform's atomic rename primitive, matching Root's platform guarantees.
func rootedRename(root *os.Root, oldName, newName string) error {
	if err := validateRenameName(oldName); err != nil {
		return &os.LinkError{Op: "renameat", Old: oldName, New: newName, Err: err}
	}
	if err := validateRenameName(newName); err != nil {
		return &os.LinkError{Op: "renameat", Old: oldName, New: newName, Err: err}
	}
	oldParent, err := openRootedDirectory(root, path.Dir(oldName))
	if err != nil {
		return &os.LinkError{Op: "renameat", Old: oldName, New: newName, Err: err}
	}
	defer oldParent.Close()
	newParent, err := openRootedDirectory(root, path.Dir(newName))
	if err != nil {
		return &os.LinkError{Op: "renameat", Old: oldName, New: newName, Err: err}
	}
	defer newParent.Close()
	if err := renameAt(oldParent, path.Base(oldName), newParent, path.Base(newName)); err != nil {
		return &os.LinkError{Op: "renameat", Old: oldName, New: newName, Err: err}
	}
	return nil
}

// openRootedDirectory appends a final dot so Root must resolve the requested
// path as a directory rather than opening a FIFO or device as the rename parent.
func openRootedDirectory(root *os.Root, name string) (*os.File, error) {
	if name == "." {
		return root.Open(name)
	}
	return root.Open(name + "/.")
}

// validateRenameName prevents path cleaning from turning a root mutation or
// traversal attempt into a valid parent-and-base pair.
func validateRenameName(name string) error {
	if name == "" || name == "." {
		return fmt.Errorf("%w: invalid rename path", storagecore.ErrForbidden)
	}
	return validateRootedPath(name)
}

// validateRootedPath rejects syntax that could acquire different parent
// semantics when passed from slash-based logical paths to an operating system.
func validateRootedPath(name string) error {
	cleaned := path.Clean(name)
	if path.IsAbs(name) || !filepath.IsLocal(name) || cleaned != name || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.ContainsAny(name, "\\\x00") || runtime.GOOS == "windows" && strings.ContainsRune(name, '?') {
		return fmt.Errorf("%w: invalid rooted path", storagecore.ErrForbidden)
	}
	return nil
}
