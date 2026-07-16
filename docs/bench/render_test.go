//go:build benchrender
// +build benchrender

package bench

import "testing"

// TestRenderBenchmarks exposes benchmark documentation generation behind the benchrender tag.
func TestRenderBenchmarks(t *testing.T) {
	RenderBenchmarks()
}
