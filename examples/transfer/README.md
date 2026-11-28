# Verified 100 GB transfer

The full run uploads 100 GB of synthetic data per selected driver and reads
all 100 GB back. It compares the byte count and SHA-256 of each read, then deletes
that object before writing the next one. The default object size is 32 MiB.
The test exercises sustained transfer without requiring 100 GB of storage
capacity at once. It uses multiple objects and verifies them individually.

## Recorded run

On November 27, 2026, Go 1.27.0 on Linux amd64 verified **100 GB uploaded and
100 GB downloaded per fixture** across eight fixtures. Each object was verified
with SHA-256. Objects were up to 32 MiB. SFTP, S3, and GCS used four concurrent
25 GB clients each. Elapsed time is the slowest client for a sharded fixture.
The runs shared an Intel Core i7-12700 host and overlapped.

| Driver | Fixture | Clients | Verified objects | Elapsed |
| --- | --- | ---: | ---: | ---: |
| local | Local filesystem | 1 | 2,981 | 6m 28s |
| memory | In-process memory | 1 | 2,981 | 2m 26s |
| redis | Redis 7.4 | 1 | 2,981 | 6m 00s |
| ftp | vsftpd | 1 | 2,981 | 5m 55s |
| sftp | OpenSSH | 4 | 2,984 | 6m 25s |
| s3 | RustFS 1.0.6, streaming | 4 | 2,984 | 7m 11s |
| gcs | fake-gcs-server 1.56.1 | 4 | 2,984 | 9m 12s |
| rclone | Rclone local remote | 1 | 2,981 | 4m 09s |

Dropbox requires a live account and was skipped in this run.

### Local write timing

The local driver syncs each file before atomically replacing the target.
Rclone's local `Put` uses buffered writes to the target. This transfer test
reads and deletes each object immediately, which favors writes that can stay
in the OS cache.

A separate ext4 backup prototype retained eight 128 MiB files, verified every
checksum, and synced the files and directories before reporting success.
Median elapsed time over three runs was 1.11 s with serial writes and per-file
sync, 0.57 s with four concurrent writers and per-file sync, and 1.06 s with
serial writes and four workers syncing at commit.

## Run the fixtures

From the repository root with Go 1.26 or newer and Docker Compose:

```sh
docker compose -f examples/transfer/compose.yaml up -d --wait
curl -fsS --retry 5 --retry-connrefused \
  -H 'Content-Type: application/json' -d '{"name":"storage-transfer"}' \
  'http://127.0.0.1:14443/storage/v1/b?project=storage-transfer'
```

The Compose stack starts Redis, vsftpd, OpenSSH SFTP, RustFS, and a GCS
emulator. Local, memory, and Rclone's local backend run in the Go process.

```sh
# Smoke test with 100 MB per driver.
go run ./examples/transfer -driver all

# Upload and verify 100 GB per driver. Use the S3 streaming API against RustFS.
go run ./examples/transfer -driver all -bytes 100000000000 -s3-stream

docker compose -f examples/transfer/compose.yaml down -v
```

Select a single driver or a comma-separated list with `-driver`. Use
`-scratch` to place local and Rclone scratch files on a specific volume.
The runner prints verified bytes, elapsed time, and upload and download
throughput. Download timing includes checksum verification. Each run uses
unique object names. On interruption the runner attempts to delete the
current object before exiting.

## Dropbox

The Docker run skips Dropbox. For a live Dropbox test, set `STORAGE_TRANSFER_DROPBOX_TOKEN` in the
process environment and optionally set `STORAGE_TRANSFER_DROPBOX_PREFIX` to
an existing test folder. Start with `go run ./examples/transfer -driver dropbox`.
The full run uses the same `-bytes 100000000000` flag and counts toward provider
quotas. The token needs upload, download, metadata, and delete permissions.

## What this covers

The shared path exercises `Put`, `Get`, and `Delete`. `-s3-stream` uses
`PutObjectContext` and `OpenObjectContext` for the same workload. The GCS row
uses an emulator, and Rclone uses a local remote. The results describe the
tested fixtures and workload. They do not measure multi-node durability or
100 GB single-object support.
