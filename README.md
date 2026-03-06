<p align="center">
  <img src="./docs/images/logo.png?v=2" width="300" alt="storage logo">
</p>

<p align="center">
  An opinionated, testable storage abstraction for Go. Laravel-inspired in product model, Go-native in API design.
</p>

<p align="center">
  Small surface area. Explicit drivers. Shared contract tests.
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/goforj/storage"><img src="https://pkg.go.dev/badge/github.com/goforj/storage.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://github.com/goforj/storage/actions"><img src="https://github.com/goforj/storage/actions/workflows/test.yml/badge.svg" alt="Go Test"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.24+-blue?logo=go" alt="Go version"></a>
</p>

## Overview

`storage` provides a small API over multiple storage backends:

```go
type Storage interface {
    Get(ctx context.Context, p string) ([]byte, error)
    Put(ctx context.Context, p string, contents []byte) error
    Delete(ctx context.Context, p string) error
    Exists(ctx context.Context, p string) (bool, error)
    List(ctx context.Context, p string) ([]Entry, error)
    URL(ctx context.Context, p string) (string, error)
}
```

What it is:
- named disks via `storage.Manager`
- direct single-disk construction via `storage.Build`
- typed direct constructors in driver modules
- explicit driver imports with blank-import registration
- shared cross-driver contract tests in `storagetest`
- centralized integration coverage in `integration`

What it is not:
- a POSIX filesystem abstraction
- a kitchen-sink package that forces every driver dependency into the root module

## Modules

The repository is organized as one product with multiple Go modules:

- `github.com/goforj/storage`
- `github.com/goforj/storage/storagetest`
- `github.com/goforj/storage/integration`
- `github.com/goforj/storage/driver/local`
- `github.com/goforj/storage/driver/s3`
- `github.com/goforj/storage/driver/gcs`
- `github.com/goforj/storage/driver/sftp`
- `github.com/goforj/storage/driver/ftp`
- `github.com/goforj/storage/driver/dropbox`
- `github.com/goforj/storage/driver/rclone`
- `github.com/goforj/storage/examples`

This keeps the root module thin while letting consumers opt into only the drivers they use.

## Install

Root module:

```bash
go get github.com/goforj/storage
```

Then add the driver modules you need, for example:

```bash
go get github.com/goforj/storage/driver/local
go get github.com/goforj/storage/driver/s3
go get github.com/goforj/storage/driver/rclone
```

## Driver Matrix

| Driver / Backend | Kind | URL | Centralized integration | Support tier | Notes |
| --- | --- | --- | --- | --- | --- |
| <img src="https://img.shields.io/badge/local-4C8EDA?logo=files&logoColor=white" alt="local"> | Local filesystem | No | Yes, direct local fixture | <img src="https://img.shields.io/badge/tier-1-2e7d32" alt="Tier 1"> | Good default for local development and tests. |
| <img src="https://img.shields.io/badge/s3-569A31?logo=amazons3&logoColor=white" alt="s3"> | Object storage | Yes, presigned GET | Yes, MinIO via testcontainers | <img src="https://img.shields.io/badge/tier-1-2e7d32" alt="Tier 1"> | Also suitable for S3-compatible endpoints. |
| <img src="https://img.shields.io/badge/gcs-4285F4?logo=googlecloud&logoColor=white" alt="gcs"> | Object storage | Yes, signed URL. No in emulator mode | Yes, fake-gcs-server emulator | <img src="https://img.shields.io/badge/tier-1-2e7d32" alt="Tier 1"> | Emulator-backed integration in the shared matrix. |
| <img src="https://img.shields.io/badge/sftp-1F6FEB?logo=gnu-bash&logoColor=white" alt="sftp"> | Remote filesystem | No | Yes, SFTP container via testcontainers | <img src="https://img.shields.io/badge/tier-1-2e7d32" alt="Tier 1"> | Password and key authentication supported. |
| <img src="https://img.shields.io/badge/ftp-FF8C00?logo=filezilla&logoColor=white" alt="ftp"> | Remote filesystem | No | Yes, embedded FTP fixture in shared matrix | <img src="https://img.shields.io/badge/tier-1-2e7d32" alt="Tier 1"> | Plain FTP and explicit TLS supported. |
| <img src="https://img.shields.io/badge/dropbox-0061FF?logo=dropbox&logoColor=white" alt="dropbox"> | Object storage | Yes, temporary link | No | <img src="https://img.shields.io/badge/tier-2-757575" alt="Tier 2"> | Lower support tier until external integration coverage is defined. |
| <img src="https://img.shields.io/badge/rclone-5A45FF?logo=rclone&logoColor=white" alt="rclone"> | Breadth driver | Backend-dependent via `PublicLink` | Yes, representative local fixture | <img src="https://img.shields.io/badge/tier-1-2e7d32" alt="Tier 1"> | Breadth driver, not the baseline for all semantics. |

Common contract across bundled drivers:
- `Get`, `Put`, `Delete`, `Exists`, and one-level `List`
- typed driver config and `New(...)` constructor
- manager registration for named-disk usage
- normalized `ErrNotFound`, `ErrForbidden`, and `ErrUnsupported` behavior

## Usage

Current guidance:
- use typed driver constructors for direct application code and tests
- use `storage.Manager` when you want named disks and config-driven construction
- use typed driver configs for both `storage.Manager` and `storage.Build`

### Manager and named disks

```go
package main

import (
    "context"
    "log"

    "github.com/goforj/storage"
    localdriver "github.com/goforj/storage/driver/local"
    s3driver "github.com/goforj/storage/driver/s3"
)

func main() {
    mgr, err := storage.New(storage.Config{
        Default: "assets",
        Disks: map[storage.DiskName]storage.DriverConfig{
            "assets": localdriver.Config{
                Remote: "/tmp/storage",
                Prefix: "assets",
            },
            "uploads": s3driver.Config{
                Bucket:          "app-uploads",
                Region:          "us-east-1",
                Endpoint:        "http://localhost:9000",
                AccessKeyID:     "minioadmin",
                SecretAccessKey: "minioadmin",
                UsePathStyle:    true,
                Prefix:          "uploads",
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    disk, err := mgr.Disk("assets")
    if err != nil {
        log.Fatal(err)
    }

    if err := disk.Put(context.Background(), "hello.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }
}
```

### Build a single disk from typed driver config

```go
package main

import (
    "context"
    "log"

    "github.com/goforj/storage"
    localdriver "github.com/goforj/storage/driver/local"
)

func main() {
    disk, err := storage.Build(context.Background(), localdriver.Config{
        Remote: "/tmp/storage",
        Prefix: "scratch",
    })
    if err != nil {
        log.Fatal(err)
    }

    if err := disk.Put(context.Background(), "build.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }
}
```

### Direct driver constructor

```go
package main

import (
    "context"
    "log"

    localdriver "github.com/goforj/storage/driver/local"
)

func main() {
    disk, err := localdriver.New(context.Background(), localdriver.Config{
        Remote: "/tmp/storage",
        Prefix: "scratch",
    })
    if err != nil {
        log.Fatal(err)
    }

    if err := disk.Put(context.Background(), "build.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }
}
```

### Rclone

`rclone` is back in this repository as its own driver module.

```go
package main

import (
    "context"
    "log"

    rclonedriver "github.com/goforj/storage/driver/rclone"
)

const rcloneConfig = `
[localdisk]
type = local
`

func main() {
    disk, err := rclonedriver.New(context.Background(), rclonedriver.Config{
        Remote:           "localdisk:/tmp/storage",
        Prefix:           "sandbox",
        RcloneConfigData: rcloneConfig,
    })
    if err != nil {
        log.Fatal(err)
    }

    if err := disk.Put(context.Background(), "rclone.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }
}
```

See [`examples`](./examples) for runnable examples.

Notes:
- `List` is one-level and non-recursive.
- `List(ctx, "")` lists from the disk root or prefix root.
- `Entry` currently includes `Path`, `Size`, and `IsDir`.
- `URL` returns a usable access URL when the driver supports it.
- unsupported operations return `storage.ErrUnsupported`.
- normalized cross-driver errors use `errors.Is` with `storage.ErrNotFound`, `storage.ErrForbidden`, and `storage.ErrUnsupported`.

More detail lives in [`DRIVER_SUPPORT.md`](./DRIVER_SUPPORT.md).

## Testing

Shared contract tests live in `storagetest`.

Centralized integration coverage lives in `integration` and runs the same contract across supported backends.
That centralized matrix is the authoritative integration path for the repository.

Current fixture types in the centralized matrix:
- testcontainers: `s3`, `sftp`
- emulator: `gcs`
- embedded/local fixtures: `local`, `ftp`, `rclone_local`

Examples:

```bash
GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test ./...
```

```bash
cd integration
GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test -tags=integration ./all -count=1
```

Select a single backend during integration runs:

```bash
cd integration
INTEGRATION_DRIVER=gcs GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test -tags=integration ./all -count=1
```

Make targets:

```bash
make test
make examples-test
make integration
make integration-driver gcs
```

## Status

Current repository direction:
- `storage` is the canonical root module and package name
- drivers are separate modules in the same repository
- `rclone` is supported as an in-repo driver module
- `examples` is its own module
- centralized integration coverage currently exercises `local`, `gcs`, `ftp`, `s3`, `sftp`, and representative `rclone` usage

The detailed refactor plan and tracker live in [`STORAGE_REFACTOR_PROPOSAL.md`](./STORAGE_REFACTOR_PROPOSAL.md).
