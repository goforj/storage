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
type Storage interface {
	Get(ctx context.Context, p string) ([]byte, error)
	Put(ctx context.Context, p string, contents []byte) error
	Delete(ctx context.Context, p string) error
	Exists(ctx context.Context, p string) (bool, error)
	List(ctx context.Context, p string) ([]Entry, error)
	URL(ctx context.Context, p string) (string, error)
}

// Walker is an optional capability for recursive traversal.
//
// Walk is not part of the core Storage interface because recursion has very
// different cost and behavior across backends.
type Walker interface {
	Walk(ctx context.Context, p string, fn func(Entry) error) error
}

// Entry represents an item returned by List.
//
// Path is relative to the storage namespace, not an OS-native path.
// Directory-like entries are listing artifacts, not a promise of POSIX-style
// storage semantics.
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
type DiskName string

// DriverConfig is implemented by typed driver configs such as local.Config or
// s3storage.Config. It is the public config boundary for Manager and Build.
type DriverConfig interface {
	DriverName() string
	ResolvedConfig() ResolvedConfig
}

// Config defines named disks using typed driver configs.
type Config struct {
	Default DiskName
	Disks   map[DiskName]DriverConfig
}

// ResolvedConfig is the normalized internal config passed to registered drivers.
// Users should prefer typed driver configs and treat this as registry adapter
// glue, not the primary construction API.
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
func JoinPrefix(prefix, p string) string {
	if prefix == "" {
		return p
	}
	if p == "" {
		return prefix
	}
	return path.Join(prefix, p)
}
