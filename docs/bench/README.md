# Storage benchmarks

The [README charts](../../README.md#benchmarks) cover small file operations and
an 8 MiB copy. They use local fixtures on one machine. The GCS emulator and FTP
server run in the benchmark process, so their allocations are included.
The S3, Rclone S3, Redis, and SFTP fixtures run in Docker. Dropbox has no
benchmark fixture.

## Results on v0.9.0

Go 1.27.0 on an Apple M4 Max, with `GOMAXPROCS=4`. The tables show medians
of six samples per case. Allocated bytes per operation come from Go's benchmark
counters.

### 8 MiB copy

Same harness and fixtures on v0.9.0 and [the commit before the remote copy changes](https://github.com/goforj/storage/commit/9e948ec).
The S3 row uses [the earlier pre-streaming release](https://github.com/goforj/storage/commit/8d21d92)
as its baseline to include the change to server-side copy.

| Fixture | Before → v0.9.0 | Allocated per copy |
| --- | ---: | ---: |
| Rclone local | 2.76 → 12.00 ms | 16.00 MiB → 75 KiB |
| Rclone on MinIO | 26.14 → 25.34 ms | 20.20 MiB → 129 KiB |
| FTP | 4.13 → 2.92 ms | 16.07 MiB → 100 KiB |
| SFTP | 96.16 → 96.52 ms | 69.48 MiB → 19.24 MiB |
| GCS emulator | 30.76 → 11.99 ms | 56.98 MiB → 16.39 MiB |
| S3 on MinIO | 37.69 → 21.22 ms | 20.12 MiB → 82 KiB |

Rclone local copy took **4.35×** as long while allocating **99.5% fewer
bytes**. A CPU profile attributed most sample time to Rclone re-reading
the source and destination to verify checksums. Rclone on MinIO and SFTP
had no clear copy latency change. The shared small operations showed no
material latency regression against the recent baseline. The older S3
comparison includes SDK updates and found 8–16% more bytes allocated across
several small requests, with no clear latency change.

### 8 MiB S3 transfer

Both APIs ran against the same MinIO bucket on v0.9.0.

| Operation | Byte slice | Stream | Allocated per operation |
| --- | ---: | ---: | ---: |
| Download | 4.88 ms | 4.50 ms | 20.03 MiB → 32.5 KiB |
| Upload | 32.13 ms | 34.36 ms | 80.3 KiB → 111.5 KiB |

The streaming download reads the entire object. Both upload benchmarks start
with a reusable, preallocated payload. Streaming accepts a reader, so callers
can upload from a file without first materializing its contents as a byte slice.

## Reproduce

Run from the repository root with Go 1.26+ and Docker. Use the same harness on
both revisions, run them serially, and compare the Go output with `benchstat`.
Copy comparisons alternated revisions in pairs of two samples. Keep the same
`bench_test.go`, `render.go`, and MinIO Dockerfile in each worktree. Use a fixed
iteration count for FTP to limit loopback connection churn.

```sh
export GOMAXPROCS=4
BENCH_DRIVER=local,memory,gcs,rclone_local,redis,s3,rclone_s3,sftp \
  go test -C docs/bench -tags=bench -run '^$' \
  -bench '^BenchmarkStorageDrivers$' -benchtime=160ms -count=2
BENCH_DRIVER=ftp go test -C docs/bench -tags=bench -run '^$' \
  -bench '^BenchmarkStorageDrivers/ftp$' -benchtime=80x -count=2
BENCH_DRIVER=s3 go test -C docs/bench -tags=bench -run '^$' \
  -bench '^BenchmarkS3Objects$' -benchtime=250ms -count=6
```

Regenerate the SVGs and README embeds from a new measurement with
`BENCH_WITH_DOCKER=1 make bench-render`. Use
`BENCH_RENDER_ONLY=1 make bench-render` to reproduce the charts from the
[checked-in rows](benchmarks_rows.json).

The plots use a time limit per driver, with an additional 80-iteration cap for
FTP. The iteration chart shows how many operations were sampled. Writes reuse
fixed keys to keep fixture size bounded. `delete` includes the write that seeds
the target. Copy and streaming setup is outside the timed loop.
