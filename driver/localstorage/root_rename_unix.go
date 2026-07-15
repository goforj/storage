//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package localstorage

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameAt uses open directory descriptors so path resolution cannot be
// redirected between validation and the atomic filesystem operation.
func renameAt(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	return unix.Renameat(int(oldParent.Fd()), oldName, int(newParent.Fd()), newName)
}
