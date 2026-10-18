package storage

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// streamFixture exposes the optional capability without adding it to the base Storage interface.
type streamFixture struct{ stubFS }

// WithContext preserves the fixture capability across public bindings.
func (s *streamFixture) WithContext(context.Context) Storage { return s }

// WalkObjectsContext keeps the fixture focused on capability discovery.
func (*streamFixture) WalkObjectsContext(context.Context, string, func(storagecore.ObjectInfo) error) error {
	return nil
}

// OpenObjectContext keeps the fixture focused on capability discovery.
func (*streamFixture) OpenObjectContext(context.Context, string, storagecore.OpenObjectOptions) (*storagecore.ObjectReader, error) {
	return nil, nil
}

// PutObjectContext keeps the fixture focused on capability discovery.
func (*streamFixture) PutObjectContext(context.Context, string, io.Reader, storagecore.PutObjectOptions) (storagecore.ObjectInfo, error) {
	return storagecore.ObjectInfo{}, nil
}

// TestObjectsCapabilityPreservesExistingStorage verifies supported, unsupported, and failed bindings.
func TestObjectsCapabilityPreservesExistingStorage(t *testing.T) {
	stream := &streamFixture{}
	for _, disk := range []Storage{stream, wrapStorage(stream), wrapStorage(stream).WithContext(context.Background())} {
		got, err := Objects(disk)
		if err != nil || got != stream {
			t.Fatal("streaming capability lost", got, err)
		}
	}
	for _, disk := range []Storage{nil, &stubFS{}, wrapStorage(&stubFS{})} {
		if _, err := Objects(disk); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
	}
	failure := errors.New("binding failed")
	if _, err := Objects(&boundStorage{inner: stream, bindErr: failure}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
