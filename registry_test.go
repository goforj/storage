package storage

import (
	"context"
	"testing"
)

// TestRegisterDriverDuplicatePanics protects the process-wide registry from silent replacement.
func TestRegisterDriverDuplicatePanics(t *testing.T) {
	name := uniqueTestDriverName("stub-duplicate")
	RegisterDriver(name, func(_ context.Context, _ ResolvedConfig) (Storage, error) {
		return stubFS{}, nil
	})
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic on duplicate registration")
		}
	}()
	RegisterDriver(name, func(_ context.Context, _ ResolvedConfig) (Storage, error) {
		return stubFS{}, nil
	})
}

// TestRegisterDriverNilFactoryPanics rejects unusable registry entries at the public boundary.
func TestRegisterDriverNilFactoryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil factory")
		}
	}()
	RegisterDriver(uniqueTestDriverName("nil-factory"), nil)
}
