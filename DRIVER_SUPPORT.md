# Driver Support

This document describes the current support level and capability differences for bundled drivers.

## Semantics

Core semantics today:
- `List(ctx, path)` is one-level and non-recursive
- `List(ctx, "")` lists from the disk root or prefix root
- `Entry` currently includes `Path`, `Size`, and `IsDir`
- `URL(ctx, path)` returns a usable access URL when supported
- unsupported URL generation returns `storage.ErrUnsupported`
- path normalization rejects traversal attempts and wraps them with `storage.ErrForbidden`
- missing objects should be detectable with `errors.Is(err, storage.ErrNotFound)`

Planned but not currently part of the built-in driver set:
- optional `Walk` capability interface

## Support Tiers

### Tier 1

Actively validated in the centralized integration matrix in [`integration/all`](./integration/all).

- `local`
- `s3`
- `gcs`
- `sftp`
- `ftp`
- representative `rclone` local usage

### Tier 2

Bundled and unit-tested, but not currently covered by the centralized integration matrix.

- `dropbox`

## Capability Matrix

| Driver | Module | URL | ModTime helper | Centralized integration | Notes |
| --- | --- | --- | --- | --- | --- |
| `local` | `github.com/goforj/storage/driver/localstorage` | No | Yes | Yes | Local storage rooted at `Remote` |
| `s3` | `github.com/goforj/storage/driver/s3storage` | Yes, presigned GET | No | Yes | Works well with MinIO and S3-compatible endpoints |
| `gcs` | `github.com/goforj/storage/driver/gcsstorage` | Yes, signed URL. No in emulator mode | No | Yes | Emulator-backed integration uses fake-gcs-server |
| `sftp` | `github.com/goforj/storage/driver/sftpstorage` | No | No | Yes | Supports password and key auth |
| `ftp` | `github.com/goforj/storage/driver/ftpstorage` | No | No | Yes | Plain FTP or explicit TLS |
| `dropbox` | `github.com/goforj/storage/driver/dropboxstorage` | Yes, temporary link | No | No | Current support tier is lower until external integration strategy is settled |
| `rclone` | `github.com/goforj/storage/driver/rclonestorage` | Backend-dependent via `PublicLink` | Yes | Yes, representative local fixture | Breadth driver, not the baseline quality bar for every backend |

## Rclone Positioning

`rclone` is intentionally supported as a breadth driver:
- useful when a backend has no native client worth targeting directly
- valuable as an escape hatch for operational breadth
- not the contract model for the rest of the library

The baseline product quality bar should be judged primarily by native drivers.

## Testing Posture

Current testing posture:
- driver-local unit tests remain in each driver module
- shared contract coverage lives in `storagetest`
- cross-driver integration coverage lives in `integration`

Recommended usage:
- treat Tier 1 drivers as the default production recommendation
- treat Tier 2 drivers as available but with narrower confidence until integration coverage improves
