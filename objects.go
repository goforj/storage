package storage

import "github.com/goforj/storage/storagecore"

// Objects returns a driver's optional streaming object capability without changing Storage.
// Every operation takes its own context; a handle's WithContext binding does not replace it.
// Unsupported drivers return ErrUnsupported rather than buffering a stream as a fallback.
func Objects(disk Storage) (storagecore.ObjectStorage, error) {
	return storagecore.Objects(disk)
}
