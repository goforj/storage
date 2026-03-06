<p align="center">
  <img src="./docs/images/logo.png?v=2" width="300" alt="storage logo">
</p>

<p align="center">
  One storage API for local disks, object stores, and remote filesystems.
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/goforj/storage"><img src="https://pkg.go.dev/badge/github.com/goforj/storage.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://github.com/goforj/filesystem/actions/workflows/test.yml"><img src="https://github.com/goforj/filesystem/actions/workflows/test.yml/badge.svg" alt="Go Test"></a>
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

## Install

Root module:

```bash
go get github.com/goforj/storage
```

Then add the driver modules you need, for example:

```bash
go get github.com/goforj/storage/driver/local
go get github.com/goforj/storage/driver/s3
go get github.com/goforj/storage/driver/gcs
go get github.com/goforj/storage/driver/sftp
go get github.com/goforj/storage/driver/ftp
go get github.com/goforj/storage/driver/dropbox
go get github.com/goforj/storage/driver/rclone
```

## Driver Matrix

| Driver / Backend | Kind | Key capabilities | Notes |
| ---: | --- | --- | --- |
| <img src="https://img.shields.io/badge/local-4C8EDA?logo=files&logoColor=white" alt="local"> | Local filesystem | local dev, test-friendly | Good default for local development and tests. |
| <img src="https://img.shields.io/badge/s3-569A31?logo=amazons3&logoColor=white" alt="s3"> | Object storage | `URL`, S3-compatible endpoints | MinIO-backed integration coverage in the shared matrix. |
| <img src="https://img.shields.io/badge/gcs-4285F4?logo=googlecloud&logoColor=white" alt="gcs"> | Object storage | `URL` | Emulator-backed integration coverage via fake-gcs-server. |
| <img src="https://img.shields.io/badge/sftp-1F6FEB?logo=gnu-bash&logoColor=white" alt="sftp"> | Remote filesystem | password auth, key auth | Container-backed integration coverage in the shared matrix. |
| <img src="https://img.shields.io/badge/ftp-FF8C00?logo=filezilla&logoColor=white" alt="ftp"> | Remote filesystem | plain FTP, explicit TLS | Embedded integration fixture in the shared matrix. |
| <img src="https://img.shields.io/badge/dropbox-0061FF?logo=dropbox&logoColor=white" alt="dropbox"> | Object storage | temporary links | Lower coverage currently; external integration strategy still open. |
| <img src="https://img.shields.io/badge/rclone-5A45FF?logo=rclone&logoColor=white" alt="rclone"> | Breadth driver | backend breadth, config-driven remotes | Representative local integration coverage; not the baseline for all semantics. |

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
    // Build a manager with multiple named disks.
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

    // Resolve a disk by name.
    disk, err := mgr.Disk("assets")
    if err != nil {
        log.Fatal(err)
    }

    // Put a file into the disk.
    if err := disk.Put(context.Background(), "hello.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }

    // Read the file back.
    data, err := disk.Get(context.Background(), "hello.txt")
    if err != nil {
        log.Fatal(err)
    }

    _ = data // []byte("hello")
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
    // Build a single storage disk from typed config.
    disk, err := storage.Build(context.Background(), localdriver.Config{
        Remote: "/tmp/storage",
        Prefix: "scratch",
    })
    if err != nil {
        log.Fatal(err)
    }

    // Put a file.
    if err := disk.Put(context.Background(), "build.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }

    // List files at the disk root.
    entries, err := disk.List(context.Background(), "")
    if err != nil {
        log.Fatal(err)
    }

    _ = entries // build.txt
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
    // Construct a driver directly.
    disk, err := localdriver.New(context.Background(), localdriver.Config{
        Remote: "/tmp/storage",
        Prefix: "scratch",
    })
    if err != nil {
        log.Fatal(err)
    }

    // Put a file.
    if err := disk.Put(context.Background(), "build.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }

    // Read the file back.
    data, err := disk.Get(context.Background(), "build.txt")
    if err != nil {
        log.Fatal(err)
    }

    _ = data // []byte("hello")
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
    // Build an rclone-backed disk from inline rclone config.
    disk, err := rclonedriver.New(context.Background(), rclonedriver.Config{
        Remote:           "localdisk:/tmp/storage",
        Prefix:           "sandbox",
        RcloneConfigData: rcloneConfig,
    })
    if err != nil {
        log.Fatal(err)
    }

    // Put a file through rclone.
    if err := disk.Put(context.Background(), "rclone.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }

    // List files from the disk root.
    entries, err := disk.List(context.Background(), "")
    if err != nil {
        log.Fatal(err)
    }

    _ = entries // rclone.txt
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