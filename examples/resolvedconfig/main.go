//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/storage"
)

func main() {
	// ResolvedConfig is the normalized internal config passed to registered drivers.
	// Users should prefer typed driver configs and treat this as registry adapter
	// glue, not the primary construction API.

	// Example: inspect a resolved config in a driver factory
	factory := storage.DriverFactory(func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		fmt.Println(cfg.Driver)
		// Output: memory
		return nil, nil
	})

	_, _ = factory(context.Background(), storage.ResolvedConfig{Driver: "memory"})
}
