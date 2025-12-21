package filesystem

import (
	"context"
	"testing"
)

func TestRegisterDriverDuplicatePanics(t *testing.T) {
	name := "stub-duplicate"
	RegisterDriver(name, func(_ context.Context, _ DiskConfig, _ Config) (Filesystem, error) {
		return stubFS{}, nil
	})
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic on duplicate registration")
		}
	}()
	RegisterDriver(name, func(_ context.Context, _ DiskConfig, _ Config) (Filesystem, error) {
		return stubFS{}, nil
	})
}
