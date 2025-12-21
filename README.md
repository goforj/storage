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
    <a href="https://codecov.io/gh/goforj/filesystem" ><img src="https://codecov.io/github/goforj/filesystem/graph/badge.svg?token=9KT46ZORP3"/></a>
<!-- test-count:embed:start -->
    <img src="https://img.shields.io/badge/tests-154-brightgreen" alt="Tests">
<!-- test-count:embed:end -->
    <a href="https://goreportcard.com/report/github.com/goforj/filesystem"><img src="https://goreportcard.com/badge/github.com/goforj/filesystem" alt="Go Report Card"></a>
</p>

## Install

```bash
go get github.com/goforj/filesystem
```

## Quick Start (single remote)

```go
package main

import (
    "context"
    "log"

    "github.com/goforj/filesystem"
    _ "github.com/goforj/filesystem/driver/rclone" // register rclone driver
)

const cfgText = `
[s3primary]
type = s3
provider = AWS
access_key_id = ACCESS
secret_access_key = SECRET
region = us-east-1
`

func main() {
    cfg := filesystem.Config{
        RcloneConfigData: cfgText, // or RcloneConfigPath if you have a file
        Default:          "primary",
        Disks: map[filesystem.DiskName]filesystem.DiskConfig{
            "primary": {Driver: "rclone", Remote: "s3primary:my-bucket", Prefix: "app"},
        },
    }

    mgr, err := filesystem.New(cfg)
    if err != nil {
        log.Fatal(err)
    }

    fs := mgr.Default()
    ctx := context.Background()
    if err := fs.Put(ctx, "foo.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }
    data, err := fs.Get(ctx, "foo.txt")
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("read: %s", data)
}
```

## Multiple Remotes

Define all remotes in one rclone config (file or inline) and map them to disks:

```go
cfgText := `
[s3primary]
type = s3
provider = AWS
access_key_id = ACCESS
secret_access_key = SECRET
region = us-east-1

[gcsarchive]
type = googlecloudstorage
service_account_file = /path/to/sa.json

[stagesftp]
type = sftp
host = sftp.example.com
user = deploy
pass = secret
`

cfg := filesystem.Config{
    RcloneConfigData: cfgText,
    Default:          "primary",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "primary": {Driver: "rclone", Remote: "s3primary:my-bucket", Prefix: "app"},
        "archive": {Driver: "rclone", Remote: "gcsarchive:backup"},
        "stage":   {Driver: "rclone", Remote: "stagesftp:/var/data"},
    },
}

mgr, _ := filesystem.New(cfg)
primary := mgr.Default()
archive, _ := mgr.Disk("archive")
stage, _ := mgr.Disk("stage")
```

`DiskName` is a typed alias (`type DiskName string`); using constants for disk names helps avoid typos.

## Environment-Only Remotes

Rclone supports defining remotes entirely via environment variables:

```bash
export RCLONE_CONFIG_MYREMOTE_TYPE=s3
export RCLONE_CONFIG_MYREMOTE_PROVIDER=AWS
export RCLONE_CONFIG_MYREMOTE_ACCESS_KEY_ID=ACCESS
export RCLONE_CONFIG_MYREMOTE_SECRET_ACCESS_KEY=SECRET
export RCLONE_CONFIG_MYREMOTE_REGION=us-east-1
```

Then reference it directly; no config file or inline data needed:

```go
cfg := filesystem.Config{
    Default: "store",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "store": {Driver: "rclone", Remote: "myremote:bucket"},
    },
}
mgr, _ := filesystem.New(cfg)
fs := mgr.Default()
```

Env vars take precedence over stored config; inline and path configs still work alongside env-defined remotes.

## Config Sources

### Inline Config (in-memory)

When `RcloneConfigData` is set, the config is loaded into memory (no temp file). The first init wins for the process; later inits must use the same inline data or a matching path, otherwise initialization errors. Env-defined remotes still override stored values.

### Path-Based Config

Point `RcloneConfigPath` at an existing rclone config file. Do not set `RcloneConfigData` at the same time.

### Environment Variables

Rclone treats `RCLONE_CONFIG_<REMOTE>_<KEY>` as config entries and prefers them over stored config. You can define remotes entirely via env vars (e.g., `RCLONE_CONFIG_MYREMOTE_TYPE=s3`, etc.) and reference them as `Remote: "myremote:bucket"` without providing a file or inline data. Inline/path config and env vars can coexist; env takes precedence.

## API Notes

- All rclone backends are available (imports `backend/all`).
- Errors are wrapped; classify with `errors.Is(err, filesystem.ErrNotFound|ErrForbidden|ErrUnsupported)`.
- `Prefix` scopes a disk into a subdirectory on the remote.
- `URL` returns a public link when the backend supports it; otherwise `ErrUnsupported`.
- Context cancellation/deadlines propagate; check `context.Canceled` / `context.DeadlineExceeded`.
