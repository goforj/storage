// Package storage provides a small abstraction over file-like objects stored in
// local, cloud, and remote backends.
//
// The package is Go-native in API shape: explicit drivers, named disks, and
// small interfaces with documented semantics.
//
// Preferred construction paths:
//   - direct use: call a driver module's New(ctx, Config)
//   - named disks: pass typed driver configs to storage.New
//   - single disk from generic orchestration: pass a typed driver config to Build
//
// Core semantics:
//   - List is one-level and non-recursive.
//   - List with an empty path lists from the disk root or prefix root.
//   - URL returns a usable access URL when the driver supports it.
//   - Unsupported operations should return ErrUnsupported.
//   - Missing objects should be detectable with errors.Is(err, ErrNotFound).
//   - Path normalization rejects traversal attempts with ErrForbidden.
//
// Driver registration is opt-in. Import the driver modules you need for
// manager-based construction, or call driver constructors directly.
package storage
