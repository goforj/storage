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
The runs shared an Intel Core i7-12700 host and overlapped. Elapsed time includes
upload, readback, checksum verification, and deletion, using each driver's
default write behavior.

| Driver | Fixture | Clients | Verified objects | Elapsed |
| --- | --- | ---: | ---: | ---: |
| local | Local filesystem, sync before atomic rename | 1 | 2,981 | 6m 28s |
| memory | In-process memory | 1 | 2,981 | 2m 26s |
| redis | Redis 7.4 | 1 | 2,981 | 6m 00s |
| ftp | vsftpd | 1 | 2,981 | 5m 55s |
| sftp | OpenSSH | 4 | 2,984 | 6m 25s |
| s3 | RustFS 1.0.6, streaming | 4 | 2,984 | 7m 11s |
| gcs | fake-gcs-server 1.56.1 | 4 | 2,984 | 9m 12s |
| rclone | Rclone local remote, buffered writes | 1 | 2,981 | 4m 09s |

Dropbox requires a live account and was skipped in this run.

<a id="local-write-timing"></a>

### Local uploads with matched write guarantees

The round trip above reads and deletes each object immediately, which favors
buffered writes that can stay in the OS cache.

In a follow-up on the same ext4 disk, median upload speed was **1,594 MiB/s**
for local and **1,643 MiB/s** for Rclone, a difference of about 3%. The Rclone
test wrapper staged, synced, and renamed each write to match the local driver.
These medians come from three 2 GB upload runs using 32 MiB objects.

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

### Buffered local writes versus Rclone

On the same ext4 NVMe volume, we ran each mode three times in rotating order.
Each run uploaded and verified 4 GB with 32 MiB objects, deleting each object
after verification. Buffered native writes finished within 1% of Rclone
end to end.

| Mode | Median upload | Median elapsed |
| --- | ---: | ---: |
| Native local, sync before rename | 1,603 MiB/s | 7.35 s |
| Native local, buffered | 3,626 MiB/s | 5.97 s |
| Rclone local remote | 3,796 MiB/s | 5.92 s |

Run the three modes in sequence on the same filesystem. The
`local-buffered` selector enables `localstorage.Config.BufferedWrites`.
It still stages and atomically replaces files, with the OS flushing data
asynchronously. Checksum verification confirms that readback matched what
was written. A system crash can still lose a buffered write.

```sh
go run ./examples/transfer -driver local,local-buffered,rclone \
  -bytes 4000000000 -scratch /path/to/test-volume
```

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
