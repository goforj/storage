//go:build benchrender
// +build benchrender

package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/storage"
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

// TestBenchmarkCases verifies that defaults stay Docker-free and explicit fixture lists are strict.
func TestBenchmarkCases(t *testing.T) {
	for _, test := range []struct {
		name, drivers, docker string
		want, required        []string
	}{
		{
			name: "default", docker: "0",
			want: []string{"local", "memory", "gcs", "ftp", "rclone_local"},
		},
		{
			name: "explicit", drivers: " ftp, s3 ", docker: "0",
			want: []string{"ftp", "s3"}, required: []string{"ftp", "s3"},
		},
		{
			name: "docker", drivers: "all", docker: "1",
			want: []string{"local", "memory", "gcs", "ftp", "rclone_local", "redis", "s3", "rclone_s3", "sftp"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BENCH_DRIVER", test.drivers)
			t.Setenv("BENCH_WITH_DOCKER", test.docker)
			cases := benchmarkCases(context.Background())
			if got := caseNames(cases); !slices.Equal(got, test.want) {
				t.Fatalf("selected fixtures = %v, want %v", got, test.want)
			}
			for _, c := range cases {
				if c.required != slices.Contains(test.required, c.name) {
					t.Errorf("%s required = %t", c.name, c.required)
				}
			}
		})
	}
}

// TestReportBenchmarkFailure verifies explicit failures stop chart generation.
func TestReportBenchmarkFailure(t *testing.T) {
	err := errors.New("copy failed")
	op := benchmarkOp{name: "copy_8mib"}
	reportBenchmarkFailure(benchmarkCase{name: "ftp"}, op, err)
	defer func() {
		failure, ok := recover().(error)
		if !ok || !errors.Is(failure, err) || !strings.Contains(failure.Error(), "ftp/copy_8mib") {
			t.Errorf("required fixture failure = %v", failure)
		}
	}()
	reportBenchmarkFailure(benchmarkCase{name: "ftp", required: true}, op, err)
}

// TestRenderFTPIterationLimit keeps the embedded FTP sampler from exhausting loopback ports.
func TestRenderFTPIterationLimit(t *testing.T) {
	t.Setenv("BENCH_RENDER_MS", "1000")
	op := benchmarkOp{
		name: "synthetic",
		run: func(context.Context, storage.Storage) error {
			return nil
		},
	}
	_, _, _, count, err := renderOp(context.Background(), "ftp", nil, op)
	if err != nil || count != ftpRenderMaxOps {
		t.Fatalf("FTP samples = %d, error = %v, want %d samples", count, err, ftpRenderMaxOps)
	}
}
