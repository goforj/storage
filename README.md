<p align="center">
  <img src="./docs/images/logo.png?v=2" width="400" alt="str logo">
</p>

<p align="center">
    A fluent, Laravel-inspired string toolkit for Go, focused on rune-safe helpers,
    expressive transformations, and predictable behavior beyond the standard library.
</p>

<p align="center">
    <a href="https://pkg.go.dev/github.com/goforj/filesystem"><img src="https://pkg.go.dev/badge/github.com/goforj/filesystem.svg" alt="Go Reference"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
    <a href="https://github.com/goforj/filesystem/actions"><img src="https://github.com/goforj/filesystem/actions/workflows/test.yml/badge.svg" alt="Go Test"></a>
    <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.18+-blue?logo=go" alt="Go version"></a>
    <img src="https://img.shields.io/github/v/tag/goforj/filesystem?label=version&sort=semver" alt="Latest tag">
    <a href="https://codecov.io/gh/goforj/filesystem" ><img src="https://codecov.io/github/goforj/filesystem/graph/badge.svg?token=BPR5IIC5F9"/></a>
<!-- test-count:embed:start -->
    <img src="https://img.shields.io/badge/tests-80-brightgreen" alt="Tests">
<!-- test-count:embed:end -->
    <a href="https://goreportcard.com/report/github.com/goforj/filesystem"><img src="https://goreportcard.com/badge/github.com/goforj/filesystem" alt="Go Report Card"></a>
</p>

## What is this?

An opinionated, testable filesystem abstraction for Go. It supports native drivers (S3, GCS, SFTP, FTP, Dropbox, local) plus an rclone-backed driver for maximum backend coverage while keeping the API tiny:

```go
Get(ctx, path) ([]byte, error)
Put(ctx, path, []byte) error
Delete(ctx, path) error
Exists(ctx, path) (bool, error)
List(ctx, path) ([]Entry, error)
URL(ctx, path) (string, error)
```

Errors are wrapped with sentinels you can classify via `errors.Is`:
`ErrNotFound`, `ErrForbidden`, `ErrUnsupported`.

## Install

```bash
go get github.com/goforj/filesystem
```

## Config & Usage

Use typed disk names and declare drivers explicitly. Rclone can be configured inline (in-memory) or via a path; env-defined remotes work too.

```go
package main

import (
    "context"
    "log"

    "github.com/goforj/filesystem"
    _ "github.com/goforj/filesystem/driver/rclone"
    _ "github.com/goforj/filesystem/driver/s3"
    _ "github.com/goforj/filesystem/driver/local"
)

const rcloneConfig = `
[myremote]
type = s3
provider = AWS
access_key_id = ACCESS
secret_access_key = SECRET
region = us-east-1
force_path_style = true
endpoint = http://localhost:4566
`

func main() {
    cfg := filesystem.Config{
        Default:          "s3",
        RcloneConfigData: rcloneConfig, // or RcloneConfigPath if you already have a file
        Disks: map[filesystem.DiskName]filesystem.DiskConfig{
            "s3": {
                Driver:            "s3",
                S3Bucket:          "bucket",
                S3Region:          "us-east-1",
                S3Endpoint:        "http://localhost:4566",
                S3AccessKeyID:     "ACCESS",
                S3SecretAccessKey: "SECRET",
                S3UsePathStyle:    true,
                Prefix:            "sandbox",
            },
            "local": {
                Driver: "local",
                Remote: "/tmp/storage",
                Prefix: "sandbox",
            },
            "rclone": {
                Driver: "rclone",
                Remote: "myremote:bucket",
                Prefix: "sandbox",
            },
        },
    }

    mgr, err := filesystem.New(cfg)
    if err != nil {
        log.Fatal(err)
    }
    fs, _ := mgr.Disk("s3")
    ctx := context.Background()
    _ = fs.Put(ctx, "folder/file.txt", []byte("hello"))
    data, _ := fs.Get(ctx, "folder/file.txt")
    _ = data
}
```

## Drivers
- `local`: local filesystem rooted at `Remote`.
- `s3`: AWS S3 (and compatibles) via AWS SDK v2.
- `gcs`: Google Cloud Storage via cloud.google.com/go/storage.
- `sftp`: via ssh + pkg/sftp.
- `ftp`: via jlaffaye/ftp.
- `dropbox`: via Dropbox SDK.
- `rclone`: all rclone backends (imports `backend/all`); config is process-global. Inline config stays in memory; no temp files.

## Rclone Config Sources
- **Inline (in-memory):** set `RcloneConfigData`; first init wins process-wide.
- **Path:** set `RcloneConfigPath`; do not combine with inline.
- **Env:** `RCLONE_CONFIG_<REMOTE>_<KEY>` env vars are honored and take precedence (e.g., `RCLONE_CONFIG_MYREMOTE_TYPE=s3`).

## Testing
- Embedded FTP and SFTP servers for hermetic tests.
- gofakes3 for native S3 tests.
- Localstack rclone S3 test opt-in via `RUN_LOCALSTACK_S3=1`.
- `go test ./...` runs contract + unit tests; example build test skips if `examples/` is absent.

## Notes
- Prefixes are normalized and guard against traversal.
- Public URLs may be unsupported depending on driver; check for `ErrUnsupported`.
- Rclone config is process-scoped; inline avoids temp files, path uses the given file.

<!-- api:embed:start -->

## API Index

| Group | Functions |
|------:|-----------|
| **Other** | [Default](#default) [Disk](#disk) [JoinPrefix](#joinprefix) [New](#new) [NormalizePath](#normalizepath) [RegisterDriver](#registerdriver) |


## Other

### <a id="default"></a>Default

Default returns the default disk or panics if misconfigured.

### <a id="disk"></a>Disk

Disk returns a named disk or an error if it does not exist.

### <a id="joinprefix"></a>JoinPrefix

JoinPrefix combines a disk prefix with a path using slash separators.

### <a id="new"></a>New

New constructs a Manager and eagerly initializes all disks.

### <a id="normalizepath"></a>NormalizePath

NormalizePath cleans a user path and rejects attempts to escape the disk root.

### <a id="registerdriver"></a>RegisterDriver

RegisterDriver makes a driver available to the Manager. It panics on duplicate registrations.
<!-- api:embed:end -->