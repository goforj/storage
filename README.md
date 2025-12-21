<p align="center">
  <img src="./docs/images/logo.png?v=2" width="400" alt="filesystem logo">
</p>

<p align="center">
    An opinionated, testable filesystem abstraction for Go - native drivers where you want control, rclone where you want breadth.
</p>

<p align="center">
    Testable. Explicit. Minimal surface area.
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

> Think “Laravel filesystem”, but Go-native, explicit, and testable.

A tiny, stable API over multiple storage backends:

```go
type Filesystem interface {
    Get(ctx context.Context, p string) ([]byte, error)
    Put(ctx context.Context, p string, contents []byte) error
    Delete(ctx context.Context, p string) error
    Exists(ctx context.Context, p string) (bool, error)
    List(ctx context.Context, p string) ([]Entry, error)
    URL(ctx context.Context, p string) (string, error)
}
```

- Native SDK drivers for control and performance.
- Rclone driver for breadth and hardened auth flows.
- Predictable errors via `errors.Is` (`ErrNotFound`, `ErrForbidden`, `ErrUnsupported`).
- Prefix handling is normalized and traversal-safe.

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

| **Driver** | Description                                             | Notes                                   |
|------:|---------------------------------------------------------|-----------------------------------------|
| **local**  | Local filesystem rooted at `Remote`                     | Prefix-scoped, traversal-safe           |
| **s3**     | AWS S3 (+ compatibles) via AWS SDK v2                   | Path-style optional; presigned URLs     |
| **gcs**    | Google Cloud Storage via cloud.google.com/go/storage    | Signed URLs; minimal metadata in `Put`  |
| **sftp**   | SFTP via ssh + pkg/sftp                                 | Host key opt-in; URL unsupported        |
| **ftp**    | FTP via [jlaffaye/ftp](https://github.com/jlaffaye/ftp) | TLS optional; URL unsupported           |
| **dropbox**| Dropbox via official SDK                                | Uses temporary links for URL            |
| **rclone** | All rclone backends (imports `backend/all`)             | Config is process-scoped                |

### Rclone Backends

See [rclone docs](https://rclone.org/overview/) for full details. Supported backends include:

| **Backend** | Notes |
|-----------:|-------|
| **amazonclouddrive** | Amazon Cloud Drive |
| **azureblob** | Microsoft Azure Blob |
| **azurefiles** | Microsoft Azure Files |
| **b2** | Backblaze B2 |
| **box** | Box |
| **cache** | Cache a remote |
| **chunker** | Transparently chunk/split large files |
| **combine** | Combine several remotes into one |
| **compress** | Compress a remote |
| **crypt** | Encrypt/decrypt a remote |
| **drive** | Google Drive |
| **dropbox** | Dropbox |
| **fichier** | 1Fichier |
| **filefabric** | Enterprise File Fabric |
| **filescom** | Files.com |
| **ftp** | FTP |
| **gcs** | Google Cloud Storage (not Drive) |
| **gphotos** | Google Photos |
| **gofile** | Gofile |
| **hasher** | Better checksums for other remotes |
| **hdfs** | Hadoop distributed file system |
| **hidrive** | HiDrive |
| **http** | HTTP |
| **imagekit** | ImageKit.io |
| **internetarchive** | Internet Archive |
| **jottacloud** | Jottacloud |
| **koofr** | Koofr and compatibles |
| **linkbox** | Linkbox |
| **local** | Local disk |
| **mailru** | Mail.ru Cloud |
| **mega** | Mega |
| **memory** | In-memory object storage |
| **netstorage** | Akamai NetStorage |
| **oos** | Oracle Object Storage |
| **onedrive** | Microsoft OneDrive |
| **opendrive** | OpenDrive |
| **pcloud** | Pcloud |
| **pikpak** | PikPak |
| **pixeldrain** | Pixeldrain |
| **premiumizeme** | premiumize.me |
| **protondrive** | Proton Drive |
| **putio** | Put.io |
| **qingstor** | QingCloud Object Storage |
| **quatrix** | Quatrix by Maytech |
| **s3** | Amazon S3-compatible providers |
| **seafile** | Seafile |
| **sftp** | SSH/SFTP |
| **sia** | Sia Decentralized Cloud |
| **smb** | SMB / CIFS |
| **storj / tardigrade** | Storj Decentralized Cloud Storage |
| **sugarsync** | Sugarsync |
| **swift** | OpenStack Swift |
| **ulozto** | Uloz.to |
| **union** | Union of multiple remotes |
| **uptobox** | Uptobox |
| **webdav** | WebDAV |
| **yandex** | Yandex Disk |
| **zoho** | Zoho |

## Driver usage examples

Minimal snippets for each driver (replace credentials/hosts as needed):

### Local
```go
_ "github.com/goforj/filesystem/driver/local"

cfg := filesystem.Config{
    Default: "local",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "local": {Driver: "local", Remote: "/tmp/storage", Prefix: "sandbox"},
    },
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("local")
ctx := context.Background()
_ = fs.Put(ctx, "file.txt", []byte("hello"))
```

### S3 (native)
```go
_ "github.com/goforj/filesystem/driver/s3"

cfg := filesystem.Config{
    Default: "s3",
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
    },
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("s3")
ctx := context.Background()
_ = fs.Put(ctx, "folder/file.txt", []byte("hello"))
```

### GCS (native)
```go
_ "github.com/goforj/filesystem/driver/gcs"

cfg := filesystem.Config{
    Default: "gcs",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "gcs": {
            Driver:             "gcs",
            GCSBucket:          "bucket",
            GCSCredentialsJSON: os.Getenv("GCS_CREDS_JSON"),
            Prefix:             "sandbox",
        },
    },
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("gcs")
ctx := context.Background()
_ = fs.Put(ctx, "file.txt", []byte("hello"))
```

### SFTP (native)
```go
_ "github.com/goforj/filesystem/driver/sftp"

cfg := filesystem.Config{
    Default: "sftp",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "sftp": {
            Driver:                   "sftp",
            SFTPHost:                 "sftp.example.com",
            SFTPPort:                 22,
            SFTPUser:                 "user",
            SFTPPassword:             "pass",
            Prefix:                   "sandbox",
            SFTPInsecureIgnoreHostKey: true, // or provide known_hosts
        },
    },
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("sftp")
ctx := context.Background()
_ = fs.Put(ctx, "file.txt", []byte("hello"))
```

### FTP (native)
```go
_ "github.com/goforj/filesystem/driver/ftp"

cfg := filesystem.Config{
    Default: "ftp",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "ftp": {
            Driver:      "ftp",
            FTPHost:     "127.0.0.1",
            FTPPort:     21,
            FTPUser:     "anonymous",
            FTPPassword: "anonymous",
            Prefix:      "sandbox",
            FTPTLS:      false,
        },
    },
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("ftp")
ctx := context.Background()
_ = fs.Put(ctx, "file.txt", []byte("hello"))
```

### Dropbox (native)
```go
_ "github.com/goforj/filesystem/driver/dropbox"

cfg := filesystem.Config{
    Default: "dropbox",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "dropbox": {
            Driver:       "dropbox",
            DropboxToken: "TOKEN",
            Prefix:       "sandbox",
        },
    },
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("dropbox")
ctx := context.Background()
_ = fs.Put(ctx, "file.txt", []byte("hello"))
```

### Rclone (any backend)
```go
_ "github.com/goforj/filesystem/driver/rclone"

const inline = `
[myremote]
type = s3
provider = AWS
access_key_id = ACCESS
secret_access_key = SECRET
region = us-east-1
force_path_style = true
endpoint = http://localhost:4566
`
cfg := filesystem.Config{
    Default:          "rclone",
    RcloneConfigData: inline, // or RcloneConfigPath, or env-only remotes
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "rclone": {Driver: "rclone", Remote: "myremote:bucket", Prefix: "sandbox"},
    },
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("rclone")
ctx := context.Background()
_ = fs.Put(ctx, "file.txt", []byte("hello"))
```

### Rclone (env-only remote)
Environment variables can define a remote without inline or path config. Rclone reads `RCLONE_CONFIG_<REMOTE>_<KEY>` where `<REMOTE>` is uppercased and `<KEY>` matches the backend option. Common S3 keys: `TYPE`, `PROVIDER`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY`, `REGION`, `ENDPOINT`, `FORCE_PATH_STYLE`. Consult the rclone backend docs for additional keys.

```bash
export RCLONE_CONFIG_ENVREMOTE_TYPE=s3
export RCLONE_CONFIG_ENVREMOTE_PROVIDER=AWS
export RCLONE_CONFIG_ENVREMOTE_ACCESS_KEY_ID=ACCESS
export RCLONE_CONFIG_ENVREMOTE_SECRET_ACCESS_KEY=SECRET
export RCLONE_CONFIG_ENVREMOTE_REGION=us-east-1
export RCLONE_CONFIG_ENVREMOTE_FORCE_PATH_STYLE=true
export RCLONE_CONFIG_ENVREMOTE_ENDPOINT=http://localhost:4566
```

```go
_ "github.com/goforj/filesystem/driver/rclone"

cfg := filesystem.Config{
    Default: "rclone",
    Disks: map[filesystem.DiskName]filesystem.DiskConfig{
        "rclone": {Driver: "rclone", Remote: "envremote:bucket", Prefix: "sandbox"},
    },
    // RcloneConfigData/Path omitted; env values define the remote
}
mgr, _ := filesystem.New(cfg)
fs, _ := mgr.Disk("rclone")
ctx := context.Background()
_ = fs.Put(ctx, "file.txt", []byte("hello"))
```

## Rclone Config Sources
- **Inline (in-memory):** set `RcloneConfigData`; first init wins process-wide.
- **Path:** set `RcloneConfigPath`; do not combine with inline.
- **Env:** `RCLONE_CONFIG_<REMOTE>_<KEY>` env vars are honored and take precedence (e.g., `RCLONE_CONFIG_MYREMOTE_TYPE=s3`).

## Testing
- Unit + contract suite: `go test ./...` (example build test skips if `examples/` is absent).
- Integration suite (docker-compose): `docker-compose up -d` then `RUN_INTEGRATION=1 go test -tags integration ./...`
  - Defaults match compose services:
    - S3/MinIO: `INTEGRATION_S3_ENDPOINT=http://localhost:9000`, access `fsuser`, secret `fspass123`, bucket `fs-integration`
    - GCS (fake-gcs-server): `INTEGRATION_GCS_ENDPOINT=http://localhost:4443`, bucket `gcs-integration`
    - SFTP: `127.0.0.1:2222` user `fsuser`, pass `pass`
    - FTP: `127.0.0.1:2121` user `fsuser`, pass `fspass` (PASV 30000-30009)
    - Rclone integration uses the same MinIO endpoint via inline config
- Localstack rclone S3 test remains opt-in via `RUN_LOCALSTACK_S3=1`.

## Notes
- Prefixes are normalized and guard against traversal.
- Public URLs may be unsupported depending on driver; check for `ErrUnsupported`.
- Rclone config is process-scoped; inline avoids temp files, path uses the given file.
