//go:build windows

package localstorage

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileRenameInformationEx selects the extended NT rename structure used for replacement semantics.
const fileRenameInformationEx = 65

// windowsFileRenameInformation describes the fallback NT rename payload for older filesystems.
type windowsFileRenameInformation struct {
	ReplaceIfExists bool
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

// windowsFileRenameInformationEx describes an NT rename with explicit replacement flags.
type windowsFileRenameInformationEx struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

// renameAt mirrors os.Root's Windows replacement semantics while keeping both
// endpoints relative to directory handles obtained through the configured Root.
func renameAt(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	objectName, err := windows.NewNTUnicodeString(oldName)
	if err != nil {
		return err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(oldParent.Fd()),
		ObjectName:    objectName,
	}
	var source windows.Handle
	err = windows.NtCreateFile(
		&source,
		windows.SYNCHRONIZE|windows.DELETE,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_DELETE|windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return windowsError(err)
	}
	defer windows.CloseHandle(source)

	encodedName, err := windows.UTF16FromString(newName)
	if err != nil {
		return err
	}
	encodedName = encodedName[:len(encodedName)-1]
	exBuffer := makeWindowsRenameBufferEx(windows.Handle(newParent.Fd()), encodedName)
	err = windows.NtSetInformationFile(source, &windows.IO_STATUS_BLOCK{}, &exBuffer[0], uint32(len(exBuffer)), fileRenameInformationEx)
	if err == nil {
		return nil
	}

	buffer := makeWindowsRenameBuffer(windows.Handle(newParent.Fd()), encodedName)
	err = windows.NtSetInformationFile(source, &windows.IO_STATUS_BLOCK{}, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
	return windowsError(err)
}

// makeWindowsRenameBuffer builds the legacy replacement structure used by
// filesystems that do not implement extended POSIX rename semantics.
func makeWindowsRenameBuffer(root windows.Handle, name []uint16) []byte {
	var layout windowsFileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+len(name)*2)
	info := (*windowsFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = true
	info.RootDirectory = root
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	return buffer
}

// makeWindowsRenameBufferEx requests replacement and POSIX semantics so an
// existing destination is handled consistently with Unix atomic rename.
func makeWindowsRenameBufferEx(root windows.Handle, name []uint16) []byte {
	var layout windowsFileRenameInformationEx
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+len(name)*2)
	info := (*windowsFileRenameInformationEx)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.RootDirectory = root
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	return buffer
}

// windowsError converts native status values to portable syscall errors so the
// driver's existing error classification remains effective on Windows.
func windowsError(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
