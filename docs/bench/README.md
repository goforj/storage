# Storage benchmarks

The [README charts](../../README.md#benchmarks) cover small file operations and
an 8 MiB copy. They use local fixtures on one machine. The GCS emulator and FTP
server run in the benchmark process, so their allocations are included.
The S3 and Rclone S3 fixtures use RustFS 1.0.6. They run in Docker alongside
Redis and SFTP. Dropbox has no benchmark fixture.

## 8 MiB S3 transfer on RustFS

Measured with RustFS 1.0.6 and Go 1.27.0 on an Apple M4 Max with
`GOMAXPROCS=4`. These are medians of six samples per operation.

| Operation | Byte slice | Stream | Allocated per operation |
| --- | ---: | ---: | ---: |
| Download | 5.60 ms | 6.02 ms | 20.03 MiB → 31.4 KiB |
| Upload | 47.98 ms | 47.21 ms | 74.5 KiB → 114.0 KiB |

The streaming download reads the entire object. Both upload benchmarks start
with a reusable, preallocated payload. Streaming accepts a reader, so callers
can upload from a file without first materializing its contents as a byte slice.

The README chart snapshot was refreshed for S3 and Rclone S3 on RustFS.
The other fixtures retain their recorded v0.9.0 measurements.

## Historical results on v0.9.0

Go 1.27.0 on an Apple M4 Max, with `GOMAXPROCS=4`. The table shows medians
of six samples per case. Allocated bytes per operation come from Go's benchmark
counters.

### 8 MiB copy

Same harness and fixtures on v0.9.0 and [the commit before the remote copy changes](https://github.com/goforj/storage/commit/9e948ec).

| Fixture | Before → v0.9.0 | Allocated per copy |
| --- | ---: | ---: |
| Rclone local | 2.76 → 12.00 ms | 16.00 MiB → 75 KiB |
| FTP | 4.13 → 2.92 ms | 16.07 MiB → 100 KiB |
| SFTP | 96.16 → 96.52 ms | 69.48 MiB → 19.24 MiB |
| GCS emulator | 30.76 → 11.99 ms | 56.98 MiB → 16.39 MiB |

Rclone local copy took **4.35×** as long while allocating **99.5% fewer
bytes**. A CPU profile attributed most sample time to Rclone re-reading
the source and destination to verify checksums. SFTP copy latency was similar
on both revisions. See the [archived v0.9.0 results](https://github.com/goforj/storage/blob/31287947eb79a601652acf386f0ba2739c83f714/docs/bench/README.md)
for the original S3 measurements and methodology.

## Reproduce

Run from the repository root with Go 1.26+ and Docker. Use the same harness on
both revisions, run them serially, and compare the Go output with `benchstat`.
For the historical comparison, use the [benchmark harness from that run](https://github.com/goforj/storage/tree/31287947eb79a601652acf386f0ba2739c83f714/docs/bench)
and its fixture in both worktrees. The copy comparison alternated revisions in
pairs of two samples. Use a fixed iteration count for FTP to limit loopback
connection churn.

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
