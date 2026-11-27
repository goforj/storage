package storagecore

import (
	"errors"
	"testing"
)

// objectTestDisk lets capability discovery distinguish a direct handle from a plain Storage.
type objectTestDisk struct {
	Storage
	ObjectStorage
}

// objectTestDecorator models decorators whose streaming support depends on their wrapped driver.
type objectTestDecorator struct {
	*objectTestDisk
	capability ObjectStorage
	err        error
}

// Objects returns the wrapped capability or explains why the decorator cannot expose it.
func (d *objectTestDecorator) Objects() (ObjectStorage, error) {
	return d.capability, d.err
}

// TestObjectsDiscovery verifies decorators control discovery even when they also implement the streaming methods.
func TestObjectsDiscovery(t *testing.T) {
	direct := &objectTestDisk{}
	if got, err := Objects(direct); got != direct || err != nil {
		t.Fatalf("direct capability = %v, %v", got, err)
	}
	decorator := &objectTestDecorator{objectTestDisk: direct, capability: direct}
	if got, err := Objects(decorator); got != direct || err != nil {
		t.Fatalf("decorated capability = %v, %v", got, err)
	}
	refusal := errors.New("streaming suspended")
	decorator.capability, decorator.err = nil, refusal
	if got, err := Objects(decorator); got != nil || !errors.Is(err, refusal) {
		t.Fatalf("disabled capability = %v, %v", got, err)
	}
	if got, err := Objects(struct{ Storage }{}); got != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("plain disk capability = %v, %v", got, err)
	}
}
