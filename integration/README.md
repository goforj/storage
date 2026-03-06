# Integration Module

This module is the repository's centralized integration test entry point.

It exists to run the same storage contract against multiple backends without scattering the primary integration story across each driver module.

## Scope

The centralized matrix currently covers:
- `local`
- `gcs`
- `ftp`
- `s3`
- `sftp`
- representative `rclone_local`

The contract itself lives in `github.com/goforj/storage/storagetest`.

## Why this module exists

This module is the default integration path because it gives the repository a single quality bar:
- one place to provision fixtures
- one place to run the same assertions across drivers
- one place to add backend selection for local development and CI

The repository should avoid parallel per-driver integration suites that simply re-run the same contract. The centralized matrix is the authoritative integration path.

## Running the full matrix

```bash
cd integration
GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test -tags=integration ./all -count=1
```

Some backends use Docker-backed fixtures. If Docker is unavailable, the affected fixtures will fail.

## Running a single backend

Use `INTEGRATION_DRIVER` to narrow the matrix:

```bash
cd integration
INTEGRATION_DRIVER=gcs GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test -tags=integration ./all -count=1
```

Multiple backends may be selected with a comma-separated list:

```bash
cd integration
INTEGRATION_DRIVER=local,gcs GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test -tags=integration ./all -count=1
```

Accepted backend selectors today:
- `local`
- `gcs`
- `ftp`
- `s3`
- `sftp`
- `rclone_local`
- `all`

## Fixtures

Current fixture model:
- `local`: temp directory
- `gcs`: fake-gcs-server
- `ftp`: embedded Go FTP server
- `s3`: MinIO via testcontainers
- `sftp`: atmoz/sftp via testcontainers
- `rclone_local`: in-memory rendered rclone local remote over a temp directory

## Adding a backend

When adding a backend to the matrix:
1. provision the smallest reliable fixture possible
2. build the backend through the same public construction path users will take
3. run `storagetest.RunStorageContractTests`
4. keep backend-specific assertions minimal unless they are part of the intended contract

If a backend cannot be covered locally or with a reliable emulator, document it explicitly instead of pretending it has the same confidence level as the rest of the matrix.
