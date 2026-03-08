package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/goforj/storage"
	ftpstorage "github.com/goforj/storage/driver/ftpstorage"
	gcsstorage "github.com/goforj/storage/driver/gcsstorage"
	localstorage "github.com/goforj/storage/driver/localstorage"
	memorystorage "github.com/goforj/storage/driver/memorystorage"
	rclonestorage "github.com/goforj/storage/driver/rclonestorage"
	s3storage "github.com/goforj/storage/driver/s3storage"
	sftpstorage "github.com/goforj/storage/driver/sftpstorage"
	"github.com/goforj/storage/storagetest"
	"github.com/goftp/server"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	benchStart = "<!-- bench:embed:start -->"
	benchEnd   = "<!-- bench:embed:end -->"
	benchRows  = "benchmarks_rows.json"
)

type benchRow struct {
	Driver   string  `json:"driver"`
	Op       string  `json:"op"`
	NsOp     float64 `json:"ns_op"`
	BytesOp  float64 `json:"bytes_op"`
	AllocsOp float64 `json:"allocs_op"`
	Ops      int64   `json:"ops"`
}

type benchmarkCase struct {
	name     string
	required bool
	setup    func(context.Context) (*benchmarkFixture, error)
}

type benchmarkOp struct {
	name  string
	setup func(context.Context, storage.Storage) error
	run   func(context.Context, storage.Storage) error
}

type benchmarkFixture struct {
	newStore func(context.Context) (storage.Storage, func(), error)
	cleanup  func()
}

var uniqueID uint64

func RenderBenchmarks() {
	root := findRoot()
	ctx := context.Background()
	rowsPath := filepath.Join(root, "docs", "bench", benchRows)
	fmt.Println("benchrender: starting")

	var rows map[string][]benchRow
	if os.Getenv("BENCH_RENDER_ONLY") == "1" {
		fmt.Println("benchrender: render-only mode from snapshot")
		loaded, err := loadBenchmarkRows(rowsPath)
		if err != nil {
			panic(fmt.Errorf("render-only mode requires %s: %w", rowsPath, err))
		}
		rows = loaded
	} else {
		fmt.Println("benchrender: collecting benchmark rows")
		rows = runBenchmarks(ctx)
		if err := saveBenchmarkRows(rowsPath, rows); err != nil {
			panic(err)
		}
	}

	fmt.Println("benchrender: writing charts")
	if err := writeDashboard(root, rows); err != nil {
		panic(err)
	}

	readmePath := filepath.Join(root, "README.md")
	fmt.Println("benchrender: updating README")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		panic(err)
	}
	updated, err := injectBenchSection(string(data), renderReadmeSection(time.Now().Unix()))
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(readmePath, []byte(updated), 0o644); err != nil {
		panic(err)
	}

	fmt.Println("✔ Benchmarks dashboard updated")
}

func benchmarkCases(ctx context.Context) []benchmarkCase {
	include := selectedBenchDrivers()
	withDocker := os.Getenv("BENCH_WITH_DOCKER") == "1"
	var cases []benchmarkCase

	add := func(name string, factory func(context.Context) (*benchmarkFixture, error)) {
		if !include(name) {
			return
		}
		cases = append(cases, benchmarkCase{
			name:     name,
			required: os.Getenv("BENCH_DRIVER") == name,
			setup:    factory,
		})
	}

	addForced := func(name string, factory func(context.Context) (*benchmarkFixture, error)) {
		cases = append(cases, benchmarkCase{
			name:     name,
			required: os.Getenv("BENCH_DRIVER") == name,
			setup:    factory,
		})
	}

	add("local", func(ctx context.Context) (*benchmarkFixture, error) {
		root, err := os.MkdirTemp("", "storage-bench-local-*")
		if err != nil {
			return nil, err
		}
		return &benchmarkFixture{
			newStore: func(context.Context) (storage.Storage, func(), error) {
				store, err := storage.Build(localstorage.Config{Root: root, Prefix: "bench"})
				return store, func() {}, err
			},
			cleanup: func() { _ = os.RemoveAll(root) },
		}, nil
	})

	add("memory", func(ctx context.Context) (*benchmarkFixture, error) {
		return &benchmarkFixture{
			newStore: func(context.Context) (storage.Storage, func(), error) {
				store, err := storage.Build(memorystorage.Config{Prefix: "bench"})
				return store, func() {}, err
			},
			cleanup: func() {},
		}, nil
	})

	add("gcs", func(ctx context.Context) (*benchmarkFixture, error) {
		host := "127.0.0.1"
		portValue, err := pickPort()
		if err != nil {
			return nil, err
		}
		port := uint16(portValue)
		server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
			Scheme:     "http",
			Host:       host,
			Port:       port,
			PublicHost: fmt.Sprintf("%s:%d", host, port),
		})
		if err != nil {
			return nil, err
		}
		bucket := "storage-bench"
		server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: bucket})
		return &benchmarkFixture{
			newStore: func(context.Context) (storage.Storage, func(), error) {
				store, err := storage.Build(gcsstorage.Config{
					Bucket:   bucket,
					Endpoint: server.URL(),
					Prefix:   "bench",
				})
				return store, func() {}, err
			},
			cleanup: func() { server.Stop() },
		}, nil
	})

	add("ftp", func(ctx context.Context) (*benchmarkFixture, error) {
		host := "127.0.0.1"
		root, err := os.MkdirTemp("", "storage-bench-ftp-*")
		if err != nil {
			return nil, err
		}
		port, err := pickPort()
		if err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
		srv, err := startEmbeddedFTPServer(host, port, root)
		if err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
		return &benchmarkFixture{
			newStore: func(context.Context) (storage.Storage, func(), error) {
				store, err := storage.Build(ftpstorage.Config{
					Host:     host,
					Port:     port,
					User:     "ftpuser",
					Password: "ftppass",
					Prefix:   "bench",
				})
				return store, func() {}, err
			},
			cleanup: func() {
				_ = srv.Shutdown()
				_ = os.RemoveAll(root)
			},
		}, nil
	})

	add("rclone_local", func(ctx context.Context) (*benchmarkFixture, error) {
		root, err := os.MkdirTemp("", "storage-bench-rclone-*")
		if err != nil {
			return nil, err
		}
		conf, err := rclonestorage.RenderLocal(rclonestorage.LocalRemote{Name: "localdisk"})
		if err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
		return &benchmarkFixture{
			newStore: func(context.Context) (storage.Storage, func(), error) {
				store, err := rclonestorage.New(rclonestorage.Config{
					Remote:           "localdisk:" + root,
					Prefix:           "bench",
					RcloneConfigData: conf,
				})
				return store, func() {}, err
			},
			cleanup: func() { _ = os.RemoveAll(root) },
		}, nil
	})

	if withDocker || include("s3") {
		addForced("s3", func(ctx context.Context) (*benchmarkFixture, error) {
			container, endpoint, err := startMinioContainer(ctx)
			if err != nil {
				return nil, err
			}
			if err := storagetest.EnsureS3Bucket(ctx, endpoint, "us-east-1", "minioadmin", "minioadmin", "storage-bench"); err != nil {
				_ = container.Terminate(ctx)
				return nil, err
			}
			return &benchmarkFixture{
				newStore: func(context.Context) (storage.Storage, func(), error) {
					store, err := storage.Build(s3storage.Config{
						Bucket:          "storage-bench",
						Region:          "us-east-1",
						Endpoint:        endpoint,
						AccessKeyID:     "minioadmin",
						SecretAccessKey: "minioadmin",
						UsePathStyle:    true,
						Prefix:          "bench",
					})
					return store, func() {}, err
				},
				cleanup: func() { _ = container.Terminate(context.Background()) },
			}, nil
		})
	}

	if withDocker || include("rclone_s3") {
		addForced("rclone_s3", func(ctx context.Context) (*benchmarkFixture, error) {
			container, endpoint, err := startMinioContainer(ctx)
			if err != nil {
				return nil, err
			}
			if err := storagetest.EnsureS3Bucket(ctx, endpoint, "us-east-1", "minioadmin", "minioadmin", "storage-bench"); err != nil {
				_ = container.Terminate(ctx)
				return nil, err
			}
			conf, err := rclonestorage.RenderS3(rclonestorage.S3Remote{
				Name:            "benchs3",
				Endpoint:        endpoint,
				Region:          "us-east-1",
				AccessKeyID:     "minioadmin",
				SecretAccessKey: "minioadmin",
				PathStyle:       true,
			})
			if err != nil {
				_ = container.Terminate(ctx)
				return nil, err
			}
			return &benchmarkFixture{
				newStore: func(context.Context) (storage.Storage, func(), error) {
					store, err := rclonestorage.New(rclonestorage.Config{
						Remote:           "benchs3:storage-bench",
						Prefix:           "bench",
						RcloneConfigData: conf,
					})
					return store, func() {}, err
				},
				cleanup: func() { _ = container.Terminate(context.Background()) },
			}, nil
		})
	}

	if withDocker || include("sftp") {
		addForced("sftp", func(ctx context.Context) (*benchmarkFixture, error) {
			container, host, port, err := startSFTPContainer(ctx)
			if err != nil {
				return nil, err
			}
			return &benchmarkFixture{
				newStore: func(context.Context) (storage.Storage, func(), error) {
					store, err := storage.Build(sftpstorage.Config{
						Host:                  host,
						Port:                  port,
						User:                  "storage",
						Password:              "storage",
						InsecureIgnoreHostKey: true,
						Prefix:                "upload/bench",
					})
					return store, func() {}, err
				},
				cleanup: func() { _ = container.Terminate(context.Background()) },
			}, nil
		})
	}

	return cases
}

func benchmarkStoreOps(b *testing.B, store storage.Storage) {
	b.Helper()
	for _, op := range benchmarkOps() {
		op := op
		b.Run(op.name, func(b *testing.B) {
			if op.setup != nil {
				if err := op.setup(context.Background(), store); err != nil {
					b.Fatalf("%s setup failed: %v", op.name, err)
				}
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := op.run(context.Background(), store); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func runBenchmarks(ctx context.Context) map[string][]benchRow {
	results := make(map[string][]benchRow)
	cases := benchmarkCases(ctx)
	fmt.Printf("benchrender: selected drivers: %s\n", strings.Join(caseNames(cases), ", "))
	for _, bc := range cases {
		driverStart := time.Now()
		fmt.Printf("benchrender: driver %s: selected\n", bc.name)
		fixture, err := bc.setup(ctx)
		if err != nil {
			if bc.required {
				panic(fmt.Errorf("%s benchmark setup failed: %w", bc.name, err))
			}
			fmt.Fprintln(os.Stderr, "benchrender: skip", bc.name+":", err)
			continue
		}
		if fixture.cleanup != nil {
			defer fixture.cleanup()
		}

		for _, op := range benchmarkOps() {
			opStart := time.Now()
			fmt.Printf("benchrender: driver %s: %s setup\n", bc.name, op.name)
			store, cleanup, err := fixture.newStore(ctx)
			if err != nil {
				fmt.Fprintln(os.Stderr, "benchrender: skip", bc.name, op.name+":", err)
				break
			}
			if op.setup != nil {
				if err := op.setup(ctx, store); err != nil {
					if cleanup != nil {
						cleanup()
					}
					fmt.Fprintln(os.Stderr, "benchrender: skip", bc.name, op.name+":", err)
					continue
				}
			}
			fmt.Printf("benchrender: driver %s: %s run\n", bc.name, op.name)
			ns, bytes, allocs, runs, err := renderOp(ctx, bc.name, store, op)
			if cleanup != nil {
				cleanup()
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "benchrender: skip", bc.name, op.name+":", err)
				break
			}
			results[op.name] = append(results[op.name], benchRow{
				Driver:   bc.name,
				Op:       op.name,
				NsOp:     ns,
				BytesOp:  bytes,
				AllocsOp: allocs,
				Ops:      runs,
			})
			fmt.Printf("benchrender: driver %s: %s done in %s\n", bc.name, op.name, time.Since(opStart).Round(time.Millisecond))
		}
		fmt.Printf("benchrender: driver %s: complete in %s\n", bc.name, time.Since(driverStart).Round(time.Millisecond))
	}
	return results
}

func benchOp(ctx context.Context, store storage.Storage, run func(*testing.B, context.Context, storage.Storage)) (float64, float64, float64, int64) {
	res := testing.Benchmark(func(b *testing.B) {
		run(b, ctx, store)
	})
	return float64(res.NsPerOp()), float64(res.AllocedBytesPerOp()), float64(res.AllocsPerOp()), int64(res.N)
}

func renderOp(ctx context.Context, driver string, store storage.Storage, op benchmarkOp) (float64, float64, float64, int64, error) {
	duration := renderDurationForDriver(driver)
	if duration <= 0 {
		duration = 250 * time.Millisecond
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	deadline := start.Add(duration)
	var ops int64
	for time.Now().Before(deadline) {
		if err := op.run(ctx, store); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("render op %s/%s failed: %w", driver, op.name, err)
		}
		ops++
	}
	if ops == 0 {
		if err := op.run(ctx, store); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("render op %s/%s failed: %w", driver, op.name, err)
		}
		ops = 1
	}
	total := time.Since(start)
	runtime.ReadMemStats(&after)

	bytesPerOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(ops)
	allocsPerOp := float64(after.Mallocs-before.Mallocs) / float64(ops)
	return float64(total.Nanoseconds()) / float64(ops), bytesPerOp, allocsPerOp, ops, nil
}

func benchmarkOps() []benchmarkOp {
	return []benchmarkOp{
		{
			name: "put_small",
			run: func(ctx context.Context, store storage.Storage) error {
				path := nextBenchPath("put")
				return putContext(store, ctx, path, []byte("hello"))
			},
		},
		{
			name: "get_small",
			setup: func(ctx context.Context, store storage.Storage) error {
				return putContext(store, ctx, "reads/get.txt", []byte("hello"))
			},
			run: func(ctx context.Context, store storage.Storage) error {
				_, err := getContext(store, ctx, "reads/get.txt")
				return err
			},
		},
		{
			name: "exists",
			setup: func(ctx context.Context, store storage.Storage) error {
				return putContext(store, ctx, "reads/exists.txt", []byte("hello"))
			},
			run: func(ctx context.Context, store storage.Storage) error {
				ok, err := existsContext(store, ctx, "reads/exists.txt")
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("expected object to exist")
				}
				return nil
			},
		},
		{
			name: "list",
			setup: func(ctx context.Context, store storage.Storage) error {
				for i := 0; i < 12; i++ {
					if err := putContext(store, ctx, fmt.Sprintf("list/entry-%02d.txt", i), []byte("hello")); err != nil {
						return err
					}
				}
				return nil
			},
			run: func(ctx context.Context, store storage.Storage) error {
				entries, err := listContext(store, ctx, "list")
				if err != nil {
					return err
				}
				if len(entries) == 0 {
					return fmt.Errorf("expected entries")
				}
				return nil
			},
		},
		{
			name: "walk",
			setup: func(ctx context.Context, store storage.Storage) error {
				paths := []string{
					"walk/a/file-1.txt",
					"walk/a/file-2.txt",
					"walk/b/c/file-3.txt",
				}
				for _, path := range paths {
					if err := putContext(store, ctx, path, []byte("hello")); err != nil {
						return err
					}
				}
				return nil
			},
			run: func(ctx context.Context, store storage.Storage) error {
				count := 0
				err := walkContext(store, ctx, "walk", func(entry storage.Entry) error {
					if !entry.IsDir {
						count++
					}
					return nil
				})
				if err != nil {
					return err
				}
				if count == 0 {
					return fmt.Errorf("expected walked entries")
				}
				return nil
			},
		},
		{
			name: "delete",
			run: func(ctx context.Context, store storage.Storage) error {
				path := nextBenchPath("delete")
				if err := putContext(store, ctx, path, []byte("hello")); err != nil {
					return err
				}
				return deleteContext(store, ctx, path)
			},
		},
	}
}

func putContext(store storage.Storage, ctx context.Context, path string, contents []byte) error {
	if cs, ok := store.(storage.ContextStorage); ok {
		return cs.PutContext(ctx, path, contents)
	}
	return store.Put(path, contents)
}

func getContext(store storage.Storage, ctx context.Context, path string) ([]byte, error) {
	if cs, ok := store.(storage.ContextStorage); ok {
		return cs.GetContext(ctx, path)
	}
	return store.Get(path)
}

func existsContext(store storage.Storage, ctx context.Context, path string) (bool, error) {
	if cs, ok := store.(storage.ContextStorage); ok {
		return cs.ExistsContext(ctx, path)
	}
	return store.Exists(path)
}

func listContext(store storage.Storage, ctx context.Context, path string) ([]storage.Entry, error) {
	if cs, ok := store.(storage.ContextStorage); ok {
		return cs.ListContext(ctx, path)
	}
	return store.List(path)
}

func walkContext(store storage.Storage, ctx context.Context, path string, fn func(storage.Entry) error) error {
	if cs, ok := store.(storage.ContextStorage); ok {
		return cs.WalkContext(ctx, path, fn)
	}
	return store.Walk(path, fn)
}

func deleteContext(store storage.Storage, ctx context.Context, path string) error {
	if cs, ok := store.(storage.ContextStorage); ok {
		return cs.DeleteContext(ctx, path)
	}
	return store.Delete(path)
}

func nextBenchPath(prefix string) string {
	n := atomic.AddUint64(&uniqueID, 1)
	return fmt.Sprintf("%s/item-%d.txt", prefix, n)
}

func selectedBenchDrivers() func(string) bool {
	want := strings.TrimSpace(strings.ToLower(os.Getenv("BENCH_DRIVER")))
	if want == "" || want == "all" {
		return func(name string) bool {
			switch strings.ToLower(name) {
			case "memory", "local", "gcs", "ftp", "rclone_local":
				return true
			default:
				return false
			}
		}
	}
	selected := map[string]bool{}
	for _, part := range strings.Split(want, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			selected[part] = true
		}
	}
	return func(name string) bool { return selected[strings.ToLower(name)] }
}

func renderDurationForDriver(driver string) time.Duration {
	if raw := strings.TrimSpace(os.Getenv("BENCH_RENDER_MS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	switch strings.ToLower(driver) {
	case "memory", "local":
		return 250 * time.Millisecond
	case "gcs", "rclone_local", "ftp":
		return 350 * time.Millisecond
	case "s3", "sftp", "rclone_s3":
		return 500 * time.Millisecond
	default:
		return 350 * time.Millisecond
	}
}

func saveBenchmarkRows(path string, rows map[string][]benchRow) error {
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func loadBenchmarkRows(path string) (map[string][]benchRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows map[string][]benchRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func renderReadmeSection(cacheBuster int64) string {
	chartPath := func(name string) string {
		return fmt.Sprintf("docs/bench/%s?t=%d", name, cacheBuster)
	}
	return strings.TrimSpace("" +
		"Benchmarks are rendered from `docs/bench` and compare the shared storage contract across representative backends.\n\n" +
		"Run the renderer with:\n\n" +
		"```bash\n" +
		"cd docs/bench\n" +
		"go test -tags benchrender . -run TestRenderBenchmarks -count=1 -v\n" +
		"```\n\n" +
		"Each chart sample uses a fixed measurement window per driver, so the ops chart remains meaningful without unbounded benchmark calibration.\n\n" +
		"Notes:\n\n" +
		"- `gcs` uses fake-gcs-server.\n" +
		"- `ftp` is excluded by default because the current driver opens a fresh connection per operation; include it with `BENCH_DRIVER=ftp`.\n" +
		"- `s3` and `sftp` use testcontainers; include them with `BENCH_WITH_DOCKER=1` or by explicitly setting `BENCH_DRIVER`.\n" +
		"- `rclone_local` measures rclone overhead on top of a local filesystem remote.\n\n" +
		"### Latency (ns/op)\n\n" +
		fmt.Sprintf("![Storage benchmark latency chart](%s)\n\n", chartPath("benchmarks_ns.svg")) +
		"### Iterations (N)\n\n" +
		fmt.Sprintf("![Storage benchmark iteration chart](%s)\n\n", chartPath("benchmarks_ops.svg")) +
		"### Allocated Bytes (B/op)\n\n" +
		fmt.Sprintf("![Storage benchmark bytes chart](%s)\n\n", chartPath("benchmarks_bytes.svg")) +
		"### Allocations (allocs/op)\n\n" +
		fmt.Sprintf("![Storage benchmark allocs chart](%s)", chartPath("benchmarks_allocs.svg")))
}

func injectBenchSection(readme, section string) (string, error) {
	start := strings.Index(readme, benchStart)
	end := strings.Index(readme, benchEnd)
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("README.md is missing benchmark embed markers")
	}
	prefix := strings.TrimRight(readme[:start+len(benchStart)], "\n")
	suffix := "\n" + strings.TrimLeft(readme[end:], "\n")
	return prefix + "\n\n" + section + suffix, nil
}

func writeDashboard(root string, rows map[string][]benchRow) error {
	charts := []struct {
		fileName string
		title    string
		value    func(benchRow) float64
	}{
		{fileName: "benchmarks_ns.svg", title: "Storage Benchmark Latency (ns/op)", value: func(row benchRow) float64 { return row.NsOp }},
		{fileName: "benchmarks_ops.svg", title: "Storage Benchmark Iterations (N)", value: func(row benchRow) float64 { return float64(row.Ops) }},
		{fileName: "benchmarks_bytes.svg", title: "Storage Benchmark Allocated Bytes (B/op)", value: func(row benchRow) float64 { return row.BytesOp }},
		{fileName: "benchmarks_allocs.svg", title: "Storage Benchmark Allocations (allocs/op)", value: func(row benchRow) float64 { return row.AllocsOp }},
	}

	for _, chart := range charts {
		if err := os.WriteFile(filepath.Join(root, "docs", "bench", chart.fileName), []byte(renderSVG(chart.title, rows, chart.value)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func renderSVG(title string, rows map[string][]benchRow, value func(benchRow) float64) string {
	drivers := orderedDrivers(rows)
	ops := orderedOps(rows)
	colors := map[string]string{
		"put_small": "#0f766e",
		"get_small": "#2563eb",
		"exists":    "#ea580c",
		"list":      "#7c3aed",
		"walk":      "#dc2626",
		"delete":    "#059669",
	}

	maxVal := 0.0
	lookup := map[string]map[string]benchRow{}
	for op, list := range rows {
		if lookup[op] == nil {
			lookup[op] = map[string]benchRow{}
		}
		for _, row := range list {
			lookup[op][row.Driver] = row
			maxVal = math.Max(maxVal, value(row))
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}
	splitScale, lowerMax := outlierSplitScale(rows, value, maxVal)

	const (
		minWidth  = 1820
		height    = 1040
		leftPad   = 240
		rightPad  = 70
		topPad    = 110
		bottomPad = 250
		groupGap  = 44
		barGap    = 10
		barWidth  = 24
		labelY    = 820
		legendY   = 900
	)
	groupWidth := len(ops)*(barWidth+barGap) + groupGap
	width := max(minWidth, leftPad+rightPad+len(drivers)*groupWidth+40)
	upperTop := topPad
	upperHeight := 0
	upperBottom := topPad
	lowerTop := topPad
	if splitScale {
		upperHeight = 170
		upperBottom = upperTop + upperHeight
		lowerTop = upperBottom + 54
	}
	lowerHeight := height - lowerTop - bottomPad

	var svg bytes.Buffer
	svg.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`+"\n", width, height, width, height))
	svg.WriteString(`<rect width="100%" height="100%" fill="#0b1020"/>` + "\n")
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="52" text-anchor="middle" fill="#f8fafc" font-size="36" font-family="Arial, sans-serif" font-weight="700">%s</text>`+"\n", width/2, title))
	if splitScale {
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="84" text-anchor="middle" fill="#94a3b8" font-size="18" font-family="Arial, sans-serif">Outliers are separated into the upper strip so the lower panel stays readable.</text>`+"\n", width/2))
	} else {
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="84" text-anchor="middle" fill="#94a3b8" font-size="18" font-family="Arial, sans-serif">Compact grouped bars by driver. Exact scale shown on the left axis.</text>`+"\n", width/2))
	}

	if splitScale {
		for i := 0; i <= 2; i++ {
			y := upperTop + int(float64(upperHeight)*float64(i)/2.0)
			v := maxVal - (maxVal-lowerMax)*(float64(i)/2.0)
			svg.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#334155" stroke-width="1"/>`+"\n", leftPad, y, width-rightPad, y))
			svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#cbd5e1" font-size="18" text-anchor="end" font-family="Arial, sans-serif">%s</text>`+"\n", leftPad-16, y+6, formatChartValue(v)))
		}
		svg.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#64748b" stroke-width="2" stroke-dasharray="8 8"/>`+"\n", leftPad, upperBottom+26, width-rightPad, upperBottom+26))
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" fill="#94a3b8" font-size="16" font-family="Arial, sans-serif">axis break</text>`+"\n", width/2, upperBottom+20))
	}

	for i := 0; i <= 4; i++ {
		y := lowerTop + int(float64(lowerHeight)*float64(i)/4.0)
		v := lowerMax * (1 - float64(i)/4.0)
		svg.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#334155" stroke-width="1"/>`+"\n", leftPad, y, width-rightPad, y))
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#cbd5e1" font-size="20" text-anchor="end" font-family="Arial, sans-serif">%s</text>`+"\n", leftPad-16, y+6, formatChartValue(v)))
	}

	for i, driver := range drivers {
		groupX := leftPad + i*groupWidth
		for j, op := range ops {
			row, ok := lookup[op][driver]
			if !ok {
				continue
			}
			v := value(row)
			lowerValue := min(v, lowerMax)
			h := int((lowerValue / lowerMax) * float64(lowerHeight))
			x := groupX + j*(barWidth+barGap)
			y := lowerTop + lowerHeight - h
			svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" rx="6" fill="%s"/>`+"\n", x, y, barWidth, h, colors[op]))
			if splitScale && v > lowerMax {
				upperValue := v - lowerMax
				upperRange := maxVal - lowerMax
				upperBarH := upperHeight
				if upperRange > 0 {
					upperBarH = max(10, int((upperValue/upperRange)*float64(upperHeight)))
				}
				upperY := upperBottom - upperBarH
				svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" rx="6" fill="%s"/>`+"\n", x, upperY, barWidth, upperBarH, colors[op]))
			}
		}
		labelX := groupX + (len(ops)*(barWidth+barGap))/2 - 8
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#f8fafc" font-size="19" text-anchor="middle" font-family="Arial, sans-serif" font-weight="700">%s</text>`+"\n", labelX, labelY, driver))
		svg.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#1e293b" stroke-width="1"/>`+"\n", groupX-12, labelY+18, groupX+len(ops)*(barWidth+barGap)-10, labelY+18))
	}

	legendX := leftPad
	for i, op := range ops {
		x := legendX + i*255
		svg.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="24" height="24" rx="5" fill="%s"/>`+"\n", x, legendY, colors[op]))
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#e2e8f0" font-size="19" font-family="Arial, sans-serif">%s</text>`+"\n", x+36, legendY+18, op))
	}

	svg.WriteString(`</svg>` + "\n")
	return svg.String()
}

func outlierSplitScale(rows map[string][]benchRow, value func(benchRow) float64, maxVal float64) (bool, float64) {
	var vals []float64
	for _, list := range rows {
		for _, row := range list {
			v := value(row)
			if v > 0 {
				vals = append(vals, v)
			}
		}
	}
	if len(vals) < 4 {
		return false, maxVal
	}
	sort.Float64s(vals)
	q1 := vals[len(vals)/4]
	q3 := vals[(len(vals)*3)/4]
	iqr := q3 - q1
	if iqr <= 0 {
		return false, maxVal
	}
	cutoff := q3 + 1.5*iqr
	if cutoff <= 0 || maxVal <= cutoff*1.35 {
		return false, maxVal
	}
	return true, cutoff
}

func formatChartValue(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.2fM", v/1_000_000)
	case v >= 10_000:
		return fmt.Sprintf("%.1fk", v/1_000)
	case v >= 1_000:
		return fmt.Sprintf("%.2fk", v/1_000)
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func orderedDrivers(rows map[string][]benchRow) []string {
	seen := map[string]bool{}
	var drivers []string
	for _, list := range rows {
		for _, row := range list {
			if !seen[row.Driver] {
				seen[row.Driver] = true
				drivers = append(drivers, row.Driver)
			}
		}
	}
	sort.Strings(drivers)
	return drivers
}

func orderedOps(rows map[string][]benchRow) []string {
	order := []string{"put_small", "get_small", "exists", "list", "walk", "delete"}
	var ops []string
	for _, op := range order {
		if len(rows[op]) > 0 {
			ops = append(ops, op)
		}
	}
	return ops
}

func caseNames(cases []benchmarkCase) []string {
	names := make([]string, 0, len(cases))
	for _, bc := range cases {
		names = append(names, bc.name)
	}
	return names
}

func findRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("repository root not found")
		}
		dir = parent
	}
}

func pickPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func startEmbeddedFTPServer(host string, port int, root string) (*server.Server, error) {
	opts := &server.ServerOpts{
		Factory:  &memFactory{root: root},
		Port:     port,
		Hostname: host,
		Auth:     &server.SimpleAuth{Name: "ftpuser", Password: "ftppass"},
		Logger:   &server.DiscardLogger{},
	}
	srv := server.NewServer(opts)
	go func() {
		_ = srv.ListenAndServe()
	}()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if storagetest.Reachable(addr) {
			return srv, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = srv.Shutdown()
	return nil, fmt.Errorf("ftp fixture did not start on %s", addr)
}

type memFactory struct {
	root string
}

func (f *memFactory) NewDriver() (server.Driver, error) {
	return &memDriver{root: f.root, perm: server.NewSimplePerm("user", "group")}, nil
}

type memDriver struct {
	root string
	perm server.Perm
}

func (d *memDriver) Init(*server.Conn) {}

func (d *memDriver) Stat(p string) (server.FileInfo, error) {
	fi, err := os.Stat(d.abs(p))
	if err != nil {
		return nil, err
	}
	return fileInfo{FileInfo: fi}, nil
}

func (d *memDriver) ChangeDir(p string) error {
	fi, err := os.Stat(d.abs(p))
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return os.ErrInvalid
	}
	return nil
}

func (d *memDriver) ListDir(p string, cb func(server.FileInfo) error) error {
	entries, err := os.ReadDir(d.abs(p))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := cb(fileInfo{FileInfo: info}); err != nil {
			return err
		}
	}
	return nil
}

func (d *memDriver) DeleteDir(p string) error  { return os.RemoveAll(d.abs(p)) }
func (d *memDriver) DeleteFile(p string) error { return os.Remove(d.abs(p)) }
func (d *memDriver) Rename(from, to string) error {
	return os.Rename(d.abs(from), d.abs(to))
}

func (d *memDriver) MakeDir(p string) error {
	return os.MkdirAll(d.abs(p), 0o755)
}

func (d *memDriver) GetFile(p string, _ int64) (int64, io.ReadCloser, error) {
	f, err := os.Open(d.abs(p))
	if err != nil {
		return 0, nil, err
	}
	info, _ := f.Stat()
	return info.Size(), f, nil
}

func (d *memDriver) PutFile(p string, r io.Reader, _ bool) (int64, error) {
	fp := d.abs(p)
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(fp)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

func (d *memDriver) abs(p string) string {
	if p == "" || p == "." {
		return d.root
	}
	return filepath.Join(d.root, p)
}

type fileInfo struct {
	os.FileInfo
}

func (f fileInfo) Owner() string { return "user" }
func (f fileInfo) Group() string { return "group" }

func startMinioContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "minio/minio:latest",
		Env:          map[string]string{"MINIO_ROOT_USER": "minioadmin", "MINIO_ROOT_PASSWORD": "minioadmin"},
		ExposedPorts: []string{"9000/tcp"},
		Cmd:          []string{"server", "/data"},
		WaitingFor:   wait.ForLog("API:").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	port, err := container.MappedPort(ctx, nat.Port("9000/tcp"))
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, fmt.Sprintf("http://%s:%s", host, port.Port()), nil
}

func startSFTPContainer(ctx context.Context) (testcontainers.Container, string, int, error) {
	req := testcontainers.ContainerRequest{
		Image:        "atmoz/sftp:latest",
		Cmd:          []string{"storage:storage:::upload"},
		ExposedPorts: []string{"22/tcp"},
		WaitingFor:   wait.ForListeningPort("22/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", 0, err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", 0, err
	}
	port, err := container.MappedPort(ctx, nat.Port("22/tcp"))
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", 0, err
	}
	parsed, err := strconv.Atoi(port.Port())
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", 0, err
	}
	return container, host, parsed, nil
}
