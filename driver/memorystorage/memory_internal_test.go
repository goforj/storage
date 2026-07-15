package memorystorage

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// TestPutContextRechecksCancellationAfterLockWait proves cancellation wins
// when it arrives after validation but before a contended mutation can run.
func TestPutContextRechecksCancellationAfterLockWait(t *testing.T) {
	d := &driver{
		dirs:    make(map[string]struct{}),
		objects: make(map[string]object),
	}
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedContext{Context: base, checked: make(chan struct{})}

	d.mu.Lock()
	result := make(chan error, 1)
	go func() {
		result <- d.PutContext(ctx, "blocked.txt", []byte("payload"))
	}()
	<-ctx.checked
	cancel()
	d.mu.Unlock()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext error = %v", err)
	}
	d.mu.RLock()
	_, exists := d.objects["blocked.txt"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("PutContext mutated storage after cancellation")
	}
}

// TestSamePathCopyPreservesObject verifies a validated no-op leaves private metadata unchanged.
func TestSamePathCopyPreservesObject(t *testing.T) {
	d := &driver{
		dirs:    make(map[string]struct{}),
		objects: make(map[string]object),
	}
	if err := d.Put("file.txt", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	before := d.objects["file.txt"].modTime
	if err := d.Copy("file.txt", "file.txt"); err != nil {
		t.Fatalf("Copy same path: %v", err)
	}
	after := d.objects["file.txt"].modTime
	if !after.Equal(before) {
		t.Fatalf("same-path Copy changed modtime from %v to %v", before, after)
	}
	if err := d.Copy("missing.txt", "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Copy missing same path error = %v", err)
	}
}

type observedContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

// Err reports the wrapped context state and exposes when the operation has
// completed its initial cancellation check.
func (c *observedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}
