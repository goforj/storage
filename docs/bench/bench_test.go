//go:build bench
// +build bench

package bench

import (
	"context"
	"testing"
)

func BenchmarkStorageDrivers(b *testing.B) {
	ctx := context.Background()
	cases := benchmarkCases(ctx)
	if len(cases) == 0 {
		b.Fatal("no benchmark cases selected")
	}

	for _, bc := range cases {
		bc := bc
		b.Run(bc.name, func(b *testing.B) {
			store, cleanup, err := bc.new(context.Background())
			if err != nil {
				if bc.required {
					b.Fatalf("%s benchmark setup failed: %v", bc.name, err)
				}
				b.Skipf("%s benchmark setup unavailable: %v", bc.name, err)
			}
			if cleanup != nil {
				b.Cleanup(cleanup)
			}
			benchmarkStoreOps(b, store)
		})
	}
}
