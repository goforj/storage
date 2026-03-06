package storage

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Storage is the public interface for interacting with a storage backend.
//
// Semantics:
//   - Put overwrites an existing object at the same path.
//   - List is one-level and non-recursive.
//   - List with an empty path lists from the disk root or prefix root.
//   - URL returns a usable access URL when the driver supports it.
//   - Unsupported operations should return ErrUnsupported.
//
// @group Core
//
// Example: use the storage interface
//
//	var disk storage.Storage
//	disk, _ = storage.Build(context.Background(), localstorage.Config{
//		Remote: "/tmp/storage-interface",
//	})
//	_ = disk
type Storage interface {
	// Get reads the object at path.
	//
	// Example: read an object
	//
	//	disk, _ := storage.Build(context.Background(), localstorage.Config{
	//		Remote: "/tmp/storage-get",
	//	})
	//	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
	//
	//	data, _ := disk.Get(context.Background(), "docs/readme.txt")
	//	fmt.Println(string(data))
	//	// Output: hello
	Get(ctx context.Context, p string) ([]byte, error)

	// Put writes an object at path, overwriting any existing object.
	//
	// Example: write an object
	//
	//	disk, _ := storage.Build(context.Background(), localstorage.Config{
	//		Remote: "/tmp/storage-put",
	//	})
	//	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
	//	fmt.Println("stored")
	//	// Output: stored
	Put(ctx context.Context, p string, contents []byte) error

	// Delete removes the object at path.
	//
	// Example: delete an object
	//
	//	disk, _ := storage.Build(context.Background(), localstorage.Config{
	//		Remote: "/tmp/storage-delete",
	//	})
	//	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
	//	_ = disk.Delete(context.Background(), "docs/readme.txt")
	//
	//	ok, _ := disk.Exists(context.Background(), "docs/readme.txt")
	//	fmt.Println(ok)
	//	// Output: false
	Delete(ctx context.Context, p string) error

	// Exists reports whether an object exists at path.
	//
	// Example: check for an object
	//
	//	disk, _ := storage.Build(context.Background(), localstorage.Config{
	//		Remote: "/tmp/storage-exists",
	//	})
	//	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
	//
	//	ok, _ := disk.Exists(context.Background(), "docs/readme.txt")
	//	fmt.Println(ok)
	//	// Output: true
	Exists(ctx context.Context, p string) (bool, error)

	// List returns the immediate children under path.
	//
	// Example: list a directory
	//
	//	disk, _ := storage.Build(context.Background(), localstorage.Config{
	//		Remote: "/tmp/storage-list",
	//	})
	//	_ = disk.Put(context.Background(), "docs/readme.txt", []byte("hello"))
	//
	//	entries, _ := disk.List(context.Background(), "docs")
	//	fmt.Println(entries[0].Path)
	//	// Output: docs/readme.txt
	List(ctx context.Context, p string) ([]Entry, error)

	// URL returns a usable access URL when the driver supports it.
	//
	// Example: handle unsupported url generation
	//
	//	disk, _ := storage.Build(context.Background(), localstorage.Config{
	//		Remote: "/tmp/storage-url",
	//	})
	//
	//	_, err := disk.URL(context.Background(), "docs/readme.txt")
	//	fmt.Println(errors.Is(err, storage.ErrUnsupported))
	//	// Output: true
	URL(ctx context.Context, p string) (string, error)
}

// Walker is an optional capability for recursive traversal.
//
// Walk is not part of the core Storage interface because recursion has very
// different cost and behavior across backends.
// @group Core
//
// Example: check for walk support
//
//	disk, _ := storage.Build(context.Background(), localstorage.Config{
//		Remote: "/tmp/storage-walk",
//	})
//
//	_, ok := disk.(storage.Walker)
//	fmt.Println(ok)
//	// Output: false
type Walker interface {
	// Walk visits entries recursively when the backend supports it.
	//
	// Example: guard and call walk when supported
	//
	//	disk, _ := storage.Build(context.Background(), localstorage.Config{
	//		Remote: "/tmp/storage-walk",
	//	})
	//
	//	walker, ok := disk.(storage.Walker)
	//	if !ok {
	//		fmt.Println("walk unsupported")
	//		return
	//	}
	//
	//	_ = walker.Walk(context.Background(), "", func(entry storage.Entry) error {
	//		fmt.Println(entry.Path)
	//		return nil
	//	})
	Walk(ctx context.Context, p string, fn func(Entry) error) error
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
type Entry struct {
	Path  string
	Size  int64
	IsDir bool
}

var (
	ErrNotFound    = errors.New("storage: not found")
	ErrForbidden   = errors.New("storage: forbidden")
	ErrUnsupported = errors.New("storage: unsupported operation")
)

// DiskName is a typed identifier for configured disks.
// @group Core
//
// Example: declare a disk name
//
//	const uploads storage.DiskName = "uploads"
//	fmt.Println(uploads)
//	// Output: uploads
type DiskName string

// DriverConfig is implemented by typed driver configs such as local.Config or
// s3storage.Config. It is the public config boundary for Manager and Build.
// @group Construction
//
// Example: pass a typed driver config
//
//	var cfg storage.DriverConfig = localstorage.Config{
//		Remote: "/tmp/storage-config",
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
//			"local": localstorage.Config{Remote: "/tmp/storage-manager"},
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
type ResolvedConfig struct {
	Driver string

	// rclone-specific (only used by rclone driver)
	Remote           string
	Prefix           string
	RcloneConfigPath string
	RcloneConfigData string

	// s3 (native)
	S3Bucket          string
	S3Endpoint        string
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3UsePathStyle    bool
	S3UnsignedPayload bool

	// sftp (native)
	SFTPHost                  string
	SFTPPort                  int
	SFTPUser                  string
	SFTPPassword              string
	SFTPKeyPath               string
	SFTPKnownHostsPath        string
	SFTPInsecureIgnoreHostKey bool

	// ftp (native)
	FTPHost               string
	FTPPort               int
	FTPUser               string
	FTPPassword           string
	FTPTLS                bool
	FTPInsecureSkipVerify bool

	// dropbox (native)
	DropboxToken string

	// gcs (native)
	GCSBucket          string
	GCSCredentialsJSON string
	GCSEndpoint        string
}

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
	cleaned := path.Clean(strings.TrimSpace(p))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		cleaned = ""
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: invalid path", ErrForbidden)
	}
	return cleaned, nil
}

// JoinPrefix combines a disk prefix with a path using slash separators.
// @group Paths
//
// Example: join a disk prefix and path
//
//	fmt.Println(storage.JoinPrefix("assets", "logo.svg"))
//	// Output: assets/logo.svg
func JoinPrefix(prefix, p string) string {
	if prefix == "" {
		return p
	}
	if p == "" {
		return prefix
	}
	return path.Join(prefix, p)
}
