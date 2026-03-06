<p align="center">
  <img src="./docs/images/logo.png?v=2" width="300" alt="storage logo">
</p>

<p align="center">
  One storage API for local disks, object stores, and remote filesystems.
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/goforj/storage"><img src="https://pkg.go.dev/badge/github.com/goforj/storage.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://github.com/goforj/storage/actions/workflows/test.yml"><img src="https://github.com/goforj/storage/actions/workflows/test.yml/badge.svg" alt="Go Test"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.24+-blue?logo=go" alt="Go version"></a>
<!-- test-count:embed:start -->
  <img src="https://img.shields.io/badge/unit_tests-11-brightgreen" alt="Unit tests (executed count)">
  <img src="https://img.shields.io/badge/integration_tests-33-blue" alt="Integration tests (executed count)">
<!-- test-count:embed:end -->
</p>

## Why

Applications often need to store files in different places:

- Local disks during development
- Object storage like S3 or GCS in production
- Remote filesystems like SFTP or FTP
- Cloud providers or custom remotes

Each backend has its own API and client library.

`storage` provides a **small, consistent interface** so your application code doesn't have to change when the backend changes.

## Install

Root module:

```bash
go get github.com/goforj/storage
```

Then add the driver modules you need, for example:

```bash
go get github.com/goforj/storage/driver/localstorage
go get github.com/goforj/storage/driver/s3storage
go get github.com/goforj/storage/driver/gcsstorage
go get github.com/goforj/storage/driver/sftpstorage
go get github.com/goforj/storage/driver/ftpstorage
go get github.com/goforj/storage/driver/dropboxstorage
go get github.com/goforj/storage/driver/rclonestorage
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
    "github.com/goforj/storage/driver/localstorage"
    "github.com/goforj/storage/driver/s3storage"
)

func main() {
    // Build a manager with multiple named disks.
    mgr, err := storage.New(storage.Config{
        Default: "assets",
        Disks: map[storage.DiskName]storage.DriverConfig{
            "assets": localstorage.Config{
                Remote: "/tmp/storage",
                Prefix: "assets",
            },
            "uploads": s3storage.Config{
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
    "github.com/goforj/storage/driver/localstorage"
)

func main() {
    // Build a single storage disk from typed config.
    disk, err := storage.Build(context.Background(), localstorage.Config{
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

### Common operations

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/goforj/storage"
    "github.com/goforj/storage/driver/localstorage"
)

func main() {
    disk, err := storage.Build(context.Background(), localstorage.Config{
        Remote: "/tmp/storage",
    })
    if err != nil {
        log.Fatal(err)
    }

    // Put a file.
    if err := disk.Put(context.Background(), "docs/readme.txt", []byte("hello")); err != nil {
        log.Fatal(err)
    }

    // Check whether the file exists.
    ok, err := disk.Exists(context.Background(), "docs/readme.txt")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(ok)
    // Output: true

    // Read the file back.
    data, err := disk.Get(context.Background(), "docs/readme.txt")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(string(data))
    // Output: hello

    // List the parent directory.
    entries, err := disk.List(context.Background(), "docs")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(entries[0].Path)
    // Output: docs/readme.txt

    // Delete the file.
    if err := disk.Delete(context.Background(), "docs/readme.txt"); err != nil {
        log.Fatal(err)
    }
}
```

### URL support

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"

    "github.com/goforj/storage"
    "github.com/goforj/storage/driver/localstorage"
)

func main() {
    disk, err := storage.Build(context.Background(), localstorage.Config{
        Remote: "/tmp/storage",
    })
    if err != nil {
        log.Fatal(err)
    }

    url, err := disk.URL(context.Background(), "docs/readme.txt")
    switch {
    case err == nil:
        fmt.Println(url)
    case errors.Is(err, storage.ErrUnsupported):
        fmt.Println("url generation unsupported")
        // Output: url generation unsupported
    default:
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

    "github.com/goforj/storage/driver/localstorage"
)

func main() {
    // Construct a driver directly.
    disk, err := localstorage.New(context.Background(), localstorage.Config{
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

    "github.com/goforj/storage/driver/rclonestorage"
)

const rcloneConfig = `
[localdisk]
type = local
`

func main() {
    // Build an rclone-backed disk from inline rclone config.
    disk, err := rclonestorage.New(context.Background(), rclonestorage.Config{
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

## API reference

The API section below is autogenerated; do not edit between the markers.

<!-- api:embed:start -->

## API Index

| Group | Functions |
|------:|-----------|
| **Config** | [rclonestorage.LocalRemote](#rclonestorage-localremote) [rclonestorage.MustRenderLocal](#rclonestorage-mustrenderlocal) [rclonestorage.MustRenderS3](#rclonestorage-mustrenders3) [rclonestorage.RenderLocal](#rclonestorage-renderlocal) [rclonestorage.RenderS3](#rclonestorage-renders3) [rclonestorage.S3Remote](#rclonestorage-s3remote) |
| **Construction** | [Build](#build) [DriverConfig](#driverconfig) [DriverFactory](#driverfactory) [ResolvedConfig](#resolvedconfig) |
| **Core** | [DiskName](#diskname) [Entry](#entry) [Storage](#storage) [Storage.Delete](#storage-delete) [Storage.Exists](#storage-exists) [Storage.Get](#storage-get) [Storage.List](#storage-list) [Storage.Put](#storage-put) [Storage.URL](#storage-url) [Walker](#walker) [Walker.Walk](#walker-walk) |
| **Drivers** | [dropboxstorage.Config](#dropboxstorage-config) [dropboxstorage.New](#dropboxstorage-new) [ftpstorage.Config](#ftpstorage-config) [ftpstorage.New](#ftpstorage-new) [gcsstorage.Config](#gcsstorage-config) [gcsstorage.New](#gcsstorage-new) [localstorage.Config](#localstorage-config) [localstorage.New](#localstorage-new) [rclonestorage.Config](#rclonestorage-config) [rclonestorage.New](#rclonestorage-new) [s3storage.Config](#s3storage-config) [s3storage.New](#s3storage-new) [sftpstorage.Config](#sftpstorage-config) [sftpstorage.New](#sftpstorage-new) |
| **Manager** | [Config](#config) [Manager](#manager) [Manager.Default](#manager-default) [Manager.Disk](#manager-disk) [New](#new) [RegisterDriver](#registerdriver) |
| **Paths** | [JoinPrefix](#joinprefix) [NormalizePath](#normalizepath) |


## Config

### <a id="rclonestorage-localremote"></a>rclonestorage.LocalRemote

LocalRemote defines a local backend configuration.

```go
remote := rclonestorage.LocalRemote{Name: "local"}
fmt.Println(remote.Name)
// Output: local
```

### <a id="rclonestorage-mustrenderlocal"></a>rclonestorage.MustRenderLocal

MustRenderLocal panics on error.

```go
cfg := rclonestorage.MustRenderLocal(rclonestorage.LocalRemote{Name: "local"})
fmt.Println(cfg)
// Output:
// [local]
// type = local
```

### <a id="rclonestorage-mustrenders3"></a>rclonestorage.MustRenderS3

MustRenderS3 panics on error.

```go
cfg := rclonestorage.MustRenderS3(rclonestorage.S3Remote{
	Name:            "assets",
	Region:          "us-east-1",
	AccessKeyID:     "key",
	SecretAccessKey: "secret",
})
fmt.Println(cfg)
// Output:
// [assets]
// type = s3
// provider = AWS
// access_key_id = key
// secret_access_key = secret
// region = us-east-1
```

### <a id="rclonestorage-renderlocal"></a>rclonestorage.RenderLocal

RenderLocal returns ini-formatted rclone config for a local backend.

```go
cfg, _ := rclonestorage.RenderLocal(rclonestorage.LocalRemote{Name: "local"})
fmt.Println(cfg)
// Output:
// [local]
// type = local
```

### <a id="rclonestorage-renders3"></a>rclonestorage.RenderS3

RenderS3 returns ini-formatted rclone config content for a single S3 remote.

```go
cfg, _ := rclonestorage.RenderS3(rclonestorage.S3Remote{
	Name:            "assets",
	Region:          "us-east-1",
	AccessKeyID:     "key",
	SecretAccessKey: "secret",
})
fmt.Println(cfg)
// Output:
// [assets]
// type = s3
// provider = AWS
// access_key_id = key
// secret_access_key = secret
// region = us-east-1
```

### <a id="rclonestorage-s3remote"></a>rclonestorage.S3Remote

S3Remote defines parameters for constructing an rclone S3 remote.

```go
remote := rclonestorage.S3Remote{
	Name:            "assets",
	Region:          "us-east-1",
	AccessKeyID:     "key",
	SecretAccessKey: "secret",
}
fmt.Println(remote.Name)
// Output: assets
```

## Construction

### <a id="build"></a>Build

Build constructs a single storage backend from a typed driver config without
a Manager.

```go
fs, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-example",
	Prefix: "assets",
})
_ = fs
```

### <a id="driverconfig"></a>DriverConfig

DriverConfig is implemented by typed driver configs such as local.Config or
s3storage.Config. It is the public config boundary for Manager and Build.

```go
var cfg storage.DriverConfig = localstorage.Config{
	Remote: "/tmp/storage-config",
}
_ = cfg
```

### <a id="driverfactory"></a>DriverFactory

DriverFactory constructs a Storage for a given normalized disk configuration.

```go
factory := storage.DriverFactory(func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	return nil, nil
})
_ = factory
```

### <a id="resolvedconfig"></a>ResolvedConfig

ResolvedConfig is the normalized internal config passed to registered drivers.
Users should prefer typed driver configs and treat this as registry adapter
glue, not the primary construction API.

```go
factory := storage.DriverFactory(func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	fmt.Println(cfg.Driver)
	// Output: memory
	return nil, nil
})

_, _ = factory(context.Background(), storage.ResolvedConfig{Driver: "memory"})
```

## Core

### <a id="diskname"></a>DiskName

DiskName is a typed identifier for configured disks.

```go
const uploads storage.DiskName = "uploads"
fmt.Println(uploads)
// Output: uploads
```

### <a id="entry"></a>Entry

Entry represents an item returned by List.

Path is relative to the storage namespace, not an OS-native path.
Directory-like entries are listing artifacts, not a promise of POSIX-style
storage semantics.

```go
entry := storage.Entry{
	Path:  "docs/readme.txt",
	Size:  5,
	IsDir: false,
}
fmt.Println(entry.Path, entry.IsDir)
// Output: docs/readme.txt false
```

### <a id="storage"></a>Storage

Storage is the public interface for interacting with a storage backend.

Semantics:
- Put overwrites an existing object at the same path.
- List is one-level and non-recursive.
- List with an empty path lists from the disk root or prefix root.
- URL returns a usable access URL when the driver supports it.
- Unsupported operations should return ErrUnsupported.

```go
var disk storage.Storage
disk, _ = storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-interface",
})
_ = disk
```

### <a id="storage-delete"></a>Storage.Delete

Delete removes the object at path.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-delete",
})
_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
_ = disk.Delete(context.Background(), "docs/readme.txt")

ok, _ := disk.Exists(context.Background(), "docs/readme.txt")
fmt.Println(ok)
// Output: false
```

### <a id="storage-exists"></a>Storage.Exists

Exists reports whether an object exists at path.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-exists",
})
_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))

ok, _ := disk.Exists(context.Background(), "docs/readme.txt")
fmt.Println(ok)
// Output: true
```

### <a id="storage-get"></a>Storage.Get

Get reads the object at path.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-get",
})
_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))

data, _ := disk.Get(context.Background(), "docs/readme.txt")
fmt.Println(string(data))
// Output: hello
```

### <a id="storage-list"></a>Storage.List

List returns the immediate children under path.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-list",
})
_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))

entries, _ := disk.List(context.Background(), "docs")
fmt.Println(entries[0].Path)
// Output: docs/readme.txt
```

### <a id="storage-put"></a>Storage.Put

Put writes an object at path, overwriting any existing object.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-put",
})
_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
fmt.Println("stored")
// Output: stored
```

### <a id="storage-url"></a>Storage.URL

URL returns a usable access URL when the driver supports it.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-url",
})

_, err := disk.URL(context.Background(), "docs/readme.txt")
fmt.Println(errors.Is(err, storage.ErrUnsupported))
// Output: true
```

### <a id="walker"></a>Walker

Walker is an optional capability for recursive traversal.

Walk is not part of the core Storage interface because recursion has very
different cost and behavior across backends.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-walk",
})

_, ok := disk.(storage.Walker)
fmt.Println(ok)
// Output: false
```

### <a id="walker-walk"></a>Walker.Walk

Walk visits entries recursively when the backend supports it.

```go
disk, _ := storage.Build(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-walk",
})

walker, ok := disk.(storage.Walker)
if !ok {
	fmt.Println("walk unsupported")
	return
}

_ = walker.Walk(context.Background(), "", func(entry storage.Entry) error {
	fmt.Println(entry.Path)
	return nil
})
```

## Drivers

### <a id="dropboxstorage-config"></a>dropboxstorage.Config

Config defines a Dropbox-backed storage disk.

```go
cfg := dropboxstorage.Config{
	Token: "token",
}
_ = cfg
```

### <a id="dropboxstorage-new"></a>dropboxstorage.New

New constructs Dropbox-backed storage using the official SDK.

```go
fs, _ := dropboxstorage.New(context.Background(), dropboxstorage.Config{
	Token: "token",
})
_ = fs
```

### <a id="ftpstorage-config"></a>ftpstorage.Config

Config defines an FTP-backed storage disk.

```go
cfg := ftpstorage.Config{
	Host:     "127.0.0.1",
	User:     "demo",
	Password: "secret",
}
_ = cfg
```

### <a id="ftpstorage-new"></a>ftpstorage.New

New constructs FTP-backed storage using jlaffaye/ftp.

```go
fs, _ := ftpstorage.New(context.Background(), ftpstorage.Config{
	Host:     "127.0.0.1",
	User:     "demo",
	Password: "secret",
})
_ = fs
```

### <a id="gcsstorage-config"></a>gcsstorage.Config

Config defines a GCS-backed storage disk.

```go
cfg := gcsstorage.Config{
	Bucket: "uploads",
}
_ = cfg
```

### <a id="gcsstorage-new"></a>gcsstorage.New

New constructs GCS-backed storage using cloud.google.com/go/storage.

```go
fs, _ := gcsstorage.New(context.Background(), gcsstorage.Config{
	Bucket: "uploads",
})
_ = fs
```

### <a id="localstorage-config"></a>localstorage.Config

Config defines local storage rooted at a filesystem path.

```go
cfg := localstorage.Config{
	Remote: "/tmp/storage-local",
	Prefix: "sandbox",
}
_ = cfg
```

### <a id="localstorage-new"></a>localstorage.New

New constructs local storage rooted at cfg.Remote with an optional prefix.

```go
fs, _ := localstorage.New(context.Background(), localstorage.Config{
	Remote: "/tmp/storage-local",
	Prefix: "sandbox",
})
_ = fs
```

### <a id="rclonestorage-config"></a>rclonestorage.Config

Config defines an rclone-backed storage disk.

```go
cfg := rclonestorage.Config{
	Remote: "local:",
	Prefix: "sandbox",
}
_ = cfg
```

### <a id="rclonestorage-new"></a>rclonestorage.New

New constructs an rclone-backed storage. All disks share a single config path.

```go
fs, _ := rclonestorage.New(context.Background(), rclonestorage.Config{
	Remote: "local:",
	Prefix: "sandbox",
})
_ = fs
```

### <a id="s3storage-config"></a>s3storage.Config

Config defines an S3-backed storage disk.

```go
cfg := s3storage.Config{
	Bucket: "uploads",
	Region: "us-east-1",
}
_ = cfg
```

### <a id="s3storage-new"></a>s3storage.New

New constructs S3-backed storage using AWS SDK v2.

```go
fs, _ := s3storage.New(context.Background(), s3storage.Config{
	Bucket: "uploads",
	Region: "us-east-1",
})
_ = fs
```

### <a id="sftpstorage-config"></a>sftpstorage.Config

Config defines an SFTP-backed storage disk.

```go
cfg := sftpstorage.Config{
	Host:     "127.0.0.1",
	User:     "demo",
	Password: "secret",
}
_ = cfg
```

### <a id="sftpstorage-new"></a>sftpstorage.New

New constructs SFTP-backed storage using ssh and pkg/sftp.

```go
fs, _ := sftpstorage.New(context.Background(), sftpstorage.Config{
	Host:     "127.0.0.1",
	User:     "demo",
	Password: "secret",
})
_ = fs
```

## Manager

### <a id="config"></a>Config

Config defines named disks using typed driver configs.

```go
cfg := storage.Config{
	Default: "local",
	Disks: map[storage.DiskName]storage.DriverConfig{
		"local": localstorage.Config{Remote: "/tmp/storage-manager"},
	},
}
_ = cfg
```

### <a id="manager"></a>Manager

Manager holds named storage disks.

```go
mgr, _ := storage.New(storage.Config{
	Default: "local",
	Disks: map[storage.DiskName]storage.DriverConfig{
		"local": localstorage.Config{Remote: "/tmp/storage-manager"},
	},
})
_ = mgr
```

### <a id="manager-default"></a>Manager.Default

Default returns the default disk or panics if misconfigured.

```go
mgr, _ := storage.New(storage.Config{
	Default: "local",
	Disks: map[storage.DiskName]storage.DriverConfig{
		"local": localstorage.Config{Remote: "/tmp/storage-default"},
	},
})

fs := mgr.Default()
fmt.Println(fs != nil)
// Output: true
```

### <a id="manager-disk"></a>Manager.Disk

Disk returns a named disk or an error if it does not exist.

```go
mgr, _ := storage.New(storage.Config{
	Default: "local",
	Disks: map[storage.DiskName]storage.DriverConfig{
		"local":   localstorage.Config{Remote: "/tmp/storage-default"},
		"uploads": localstorage.Config{Remote: "/tmp/storage-uploads"},
	},
})

fs, _ := mgr.Disk("uploads")
fmt.Println(fs != nil)
// Output: true
```

### <a id="new"></a>New

New constructs a Manager and eagerly initializes all disks.

```go
mgr, _ := storage.New(storage.Config{
	Default: "local",
	Disks: map[storage.DiskName]storage.DriverConfig{
		"local":  localstorage.Config{Remote: "/tmp/storage-local"},
		"assets": localstorage.Config{Remote: "/tmp/storage-assets", Prefix: "public"},
	},
})
_ = mgr
```

### <a id="registerdriver"></a>RegisterDriver

RegisterDriver makes a driver available to the Manager. It panics on duplicate registrations.

```go
storage.RegisterDriver("memory", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	return nil, nil
})
```

## Paths

### <a id="joinprefix"></a>JoinPrefix

JoinPrefix combines a disk prefix with a path using slash separators.

```go
fmt.Println(storage.JoinPrefix("assets", "logo.svg"))
// Output: assets/logo.svg
```

### <a id="normalizepath"></a>NormalizePath

NormalizePath cleans a user path, normalizes separators, and rejects attempts
to escape the disk root or prefix root.

The empty string and root-like inputs normalize to the logical root.

```go
p, _ := storage.NormalizePath(" /avatars//user-1.png ")
fmt.Println(p)
// Output: avatars/user-1.png
```
<!-- api:embed:end -->
