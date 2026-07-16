//go:build wasip1

package localstorage

import (
	"os"
	"syscall"
	"unsafe"
)

// renameAt invokes WASI's descriptor-relative rename so sandbox confinement is
// retained without depending on the Go 1.25 os.Root method.
func renameAt(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	errno := pathRename(
		int32(oldParent.Fd()),
		unsafe.StringData(oldName),
		uint32(len(oldName)),
		int32(newParent.Fd()),
		unsafe.StringData(newName),
		uint32(len(newName)),
	)
	if errno == 0 {
		return nil
	}
	return errno
}

// pathRename exposes the WASI primitive used by Go's own rooted rename.
//
//go:wasmimport wasi_snapshot_preview1 path_rename
//go:noescape
func pathRename(oldFD int32, oldPath *byte, oldPathLen uint32, newFD int32, newPath *byte, newPathLen uint32) syscall.Errno
