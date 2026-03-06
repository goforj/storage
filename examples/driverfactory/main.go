//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/storage"
)

func main() {
	// DriverFactory constructs a Storage for a given normalized disk configuration.

	// Example: declare a driver factory
	factory := storage.DriverFactory(func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return nil, nil
	})
	_ = factory
}
