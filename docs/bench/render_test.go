//go:build benchrender
// +build benchrender

package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderBenchmarks exposes benchmark documentation generation behind the benchrender tag.
func TestRenderBenchmarks(t *testing.T) {
	RenderBenchmarks()
}

// TestDashboardCacheBuster verifies identical SVGs keep stable URLs while changed output invalidates the cache.
func TestDashboardCacheBuster(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "docs", "bench")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"benchmarks_ns.svg", "benchmarks_ops.svg", "benchmarks_bytes.svg", "benchmarks_allocs.svg"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("<svg>"+name+"</svg>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first, err := dashboardCacheBuster(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dashboardCacheBuster(root)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("cache buster changed without an SVG change: %q != %q", first, second)
	}
	if err := os.WriteFile(filepath.Join(directory, names[0]), []byte("<svg>changed</svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := dashboardCacheBuster(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("cache buster did not change with generated SVG contents")
	}
}

// TestDashboardCacheBusterMissingChart verifies incomplete generated output fails closed.
func TestDashboardCacheBusterMissingChart(t *testing.T) {
	_, err := dashboardCacheBuster(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "read generated benchmark chart") {
		t.Fatalf("missing chart error = %v", err)
	}
}
