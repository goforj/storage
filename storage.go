package storage

import (
	"context"

	storagecore "github.com/goforj/storage/storagecore"
)

// Storage is the public interface for interacting with a storage backend.
//
// Semantics:
//   - Put overwrites an existing object at the same path.
//   - MakeDir creates a directory-like prefix and may be implemented as a
//     backend-specific directory marker on object stores.
//   - List is one-level and non-recursive.
//   - List with an empty path lists from the disk root or prefix root.
//   - Walk is recursive.
//   - URL returns a usable access URL when the driver supports it.
//   - Copy overwrites the destination object when the backend supports copy semantics.
//   - Move relocates an object or directory tree and may be implemented as copy followed by delete.
//   - Unsupported operations should return ErrUnsupported.
//
// @group Core
//
// Example: use the storage interface
//
//	var disk storage.Storage
//	disk, _ = storage.Build(localstorage.Config{
//		Root: "/tmp/storage-interface",
//	})
//	_ = disk
type Storage interface {
	// Get reads the object at path.
	//
	// Example: read an object
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-get",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//
	//	data, _ := disk.Get("docs/readme.txt")
	//	fmt.Println(string(data))
	//	// Output: hello
	Get(p string) ([]byte, error)

	// Put writes an object at path, overwriting any existing object.
	//
	// Example: write an object
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-put",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//	fmt.Println("stored")
	//	// Output: stored
	Put(p string, contents []byte) error

	// MakeDir creates a directory-like entry at path.
	//
	// Example: create a directory
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-mkdir",
	//	})
	//	_ = disk.MakeDir("docs/archive")
	MakeDir(p string) error

	// Delete removes the object at path.
	//
	// Example: delete an object
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-delete",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//	_ = disk.Delete("docs/readme.txt")
	//
	//	ok, _ := disk.Exists("docs/readme.txt")
	//	fmt.Println(ok)
	//	// Output: false
	Delete(p string) error

	// Stat returns the entry at path.
	//
	// Example: stat an object
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-stat",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//
	//	entry, _ := disk.Stat("docs/readme.txt")
	//	fmt.Println(entry.Path, entry.Size)
	//	// Output: docs/readme.txt 5
	Stat(p string) (Entry, error)

	// Exists reports whether an object exists at path.
	//
	// Example: check for an object
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-exists",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//
	//	ok, _ := disk.Exists("docs/readme.txt")
	//	fmt.Println(ok)
	//	// Output: true
	Exists(p string) (bool, error)

	// List returns the immediate children under path.
	//
	// Example: list a directory
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-list",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//
	//	entries, _ := disk.List("docs")
	//	fmt.Println(entries[0].Path)
	//	// Output: docs/readme.txt
	List(p string) ([]Entry, error)

	// Walk visits entries recursively when the backend supports it.
	//
	// Example: walk a backend when supported
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-walk",
	//	})
	//
	//	err := disk.Walk("", func(entry storage.Entry) error {
	//		fmt.Println(entry.Path)
	//		return nil
	//	})
	//	fmt.Println(errors.Is(err, storage.ErrUnsupported))
	//	// Output: true
	Walk(p string, fn func(Entry) error) error

	// Copy copies the object at src to dst.
	//
	// Example: copy an object
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-copy",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//	_ = disk.Copy("docs/readme.txt", "docs/copy.txt")
	//
	//	data, _ := disk.Get("docs/copy.txt")
	//	fmt.Println(string(data))
	//	// Output: hello
	Copy(src, dst string) error

	// Move moves the object or directory tree at src to dst.
	//
	// Example: move an object
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-move",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//	_ = disk.Move("docs/readme.txt", "docs/archive.txt")
	//
	//	ok, _ := disk.Exists("docs/readme.txt")
	//	fmt.Println(ok)
	//	// Output: false
	//
	// Example: move a directory tree
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-move-dir",
	//	})
	//	_ = disk.MakeDir("docs/archive")
	//	_ = disk.Put("docs/archive/readme.txt", []byte("hello"))
	//	_ = disk.Move("docs/archive", "docs/published")
	//
	//	entry, _ := disk.Stat("docs/published")
	//	fmt.Println(entry.IsDir)
	//	// Output: true
	Move(src, dst string) error

	// URL returns a usable access URL when the driver supports it.
	//
	// Example: request an object url
	//
	//	disk, _ := storage.Build(s3storage.Config{
	//		Bucket: "uploads",
	//		Region: "us-east-1",
	//	})
	//
	//	url, _ := disk.URL("docs/readme.txt")
	//	_ = url
	//
	// Example: handle unsupported url generation
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-url",
	//	})
	//
	//	_, err := disk.URL("docs/readme.txt")
	//	fmt.Println(errors.Is(err, storage.ErrUnsupported))
	//	// Output: true
	URL(p string) (string, error)
}

// ContextStorage exposes context-aware storage operations for cancellation and deadlines.
// Use Storage for the common path and type-assert to ContextStorage when you need caller-provided context.
// @group Context
type ContextStorage interface {
	// GetContext reads the object at path using the caller-provided context.
	//
	// Example: read an object with a timeout
	//
	//	disk, _ := storage.Build(localstorage.Config{
	//		Root: "/tmp/storage-get-context",
	//	})
	//	_ = disk.Put("docs/readme.txt", []byte("hello"))
	//
	//	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	//	defer cancel()
	//
	//	cs := disk.(storage.ContextStorage)
	//	data, _ := cs.GetContext(ctx, "docs/readme.txt")
	//	fmt.Println(string(data))
	//	// Output: hello
	GetContext(ctx context.Context, p string) ([]byte, error)
	// PutContext writes an object at path using the caller-provided context.
	PutContext(ctx context.Context, p string, contents []byte) error
	// MakeDirContext creates a directory-like entry using the caller-provided context.
	MakeDirContext(ctx context.Context, p string) error
	// DeleteContext removes the object at path using the caller-provided context.
	DeleteContext(ctx context.Context, p string) error
	// StatContext returns the entry at path using the caller-provided context.
	StatContext(ctx context.Context, p string) (Entry, error)
	// ExistsContext reports whether an object exists at path using the caller-provided context.
	ExistsContext(ctx context.Context, p string) (bool, error)
	// ListContext returns the immediate children under path using the caller-provided context.
	ListContext(ctx context.Context, p string) ([]Entry, error)
	// WalkContext visits entries recursively using the caller-provided context.
	WalkContext(ctx context.Context, p string, fn func(Entry) error) error
	// CopyContext copies the object at src to dst using the caller-provided context.
	CopyContext(ctx context.Context, src, dst string) error
	// MoveContext moves the object at src to dst using the caller-provided context.
	MoveContext(ctx context.Context, src, dst string) error
	// URLContext returns a usable access URL using the caller-provided context.
	URLContext(ctx context.Context, p string) (string, error)
}

// ListPageResult describes a paginated one-level directory listing.
// @group Context
type ListPageResult = storagecore.ListPageResult

// PagedStorage exposes paginated one-level listing when a backend can support it.
// Use Storage for the common path and type-assert to PagedStorage when you need
// backend-driven pagination.
// @group Context
type PagedStorage interface {
	// ListPage returns one page of immediate children under path.
	ListPage(p string, offset, limit int) (ListPageResult, error)
}

// ContextPagedStorage exposes context-aware paginated one-level listing.
// @group Context
type ContextPagedStorage interface {
	// ListPageContext returns one page of immediate children under path using the caller-provided context.
	ListPageContext(ctx context.Context, p string, offset, limit int) (ListPageResult, error)
}

// Entry represents an item returned by List.
//
// Path is relative to the storage namespace, not an OS-native path.
// Directory-like entries are listing artifacts, not a promise of POSIX-style
// storage semantics.
// @group Core
//
// Example: inspect a listed entry
//
//	entry := storage.Entry{
//		Path:  "docs/readme.txt",
//		Size:  5,
//		IsDir: false,
//	}
//	fmt.Println(entry.Path, entry.IsDir)
//	// Output: docs/readme.txt false
type Entry = storagecore.Entry

var (
	ErrNotFound    = storagecore.ErrNotFound
	ErrForbidden   = storagecore.ErrForbidden
	ErrUnsupported = storagecore.ErrUnsupported
)

func PaginateEntries(entries []Entry, offset, limit int) ListPageResult {
	return storagecore.PaginateEntries(entries, offset, limit)
}

// DiskName is a typed identifier for configured disks.
// @group Core
//
// Example: declare a disk name
//
//	const uploads storage.DiskName = "uploads"
//	fmt.Println(uploads)
//	// Output: uploads
type DiskName = storagecore.DiskName

// DriverConfig is implemented by typed driver configs such as local.Config or
// s3storage.Config. It is the public config boundary for Manager and Build.
// @group Construction
//
// Example: pass a typed driver config
//
//	var cfg storage.DriverConfig = localstorage.Config{
//		Root: "/tmp/storage-config",
//	}
//	_ = cfg
type DriverConfig interface {
	DriverName() string
	ResolvedConfig() ResolvedConfig
}

// Config defines named disks using typed driver configs.
// @group Manager
//
// Example: define manager config
//
//	cfg := storage.Config{
//		Default: "local",
//		Disks: map[storage.DiskName]storage.DriverConfig{
//			"local": localstorage.Config{Root: "/tmp/storage-manager"},
//		},
//	}
//	_ = cfg
type Config struct {
	Default DiskName
	Disks   map[DiskName]DriverConfig
}

// ResolvedConfig is the normalized internal config passed to registered drivers.
// Users should prefer typed driver configs and treat this as registry adapter
// glue, not the primary construction API.
// @group Construction
//
// Example: inspect a resolved config in a driver factory
//
//	factory := storage.DriverFactory(func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
//		fmt.Println(cfg.Driver)
//		// Output: memory
//		return nil, nil
//	})
//
//	_, _ = factory(context.Background(), storage.ResolvedConfig{Driver: "memory"})
type ResolvedConfig = storagecore.ResolvedConfig

// NormalizePath cleans a user path, normalizes separators, and rejects attempts
// to escape the disk root or prefix root.
//
// The empty string and root-like inputs normalize to the logical root.
// @group Paths
//
// Example: normalize a user path
//
//	p, _ := storage.NormalizePath(" /avatars//user-1.png ")
//	fmt.Println(p)
//	// Output: avatars/user-1.png
func NormalizePath(p string) (string, error) {
	return storagecore.NormalizePath(p)
}

// JoinPrefix combines a disk prefix with a path using slash separators.
// @group Paths
//
// Example: join a disk prefix and path
//
//	fmt.Println(storage.JoinPrefix("assets", "logo.svg"))
//	// Output: assets/logo.svg
func JoinPrefix(prefix, p string) string {
	return storagecore.JoinPrefix(prefix, p)
}
