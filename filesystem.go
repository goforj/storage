package filesystem

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Filesystem is the public interface for interacting with a storage backend.
type Filesystem interface {
	Get(ctx context.Context, p string) ([]byte, error)
	Put(ctx context.Context, p string, contents []byte) error

	Delete(ctx context.Context, p string) error
	Exists(ctx context.Context, p string) (bool, error)

	List(ctx context.Context, p string) ([]Entry, error)

	URL(ctx context.Context, p string) (string, error)
}

// Entry represents an item returned by List.
type Entry struct {
	Path  string
	Size  int64
	IsDir bool
}

var (
	ErrNotFound    = errors.New("filesystem: not found")
	ErrForbidden   = errors.New("filesystem: forbidden")
	ErrUnsupported = errors.New("filesystem: unsupported operation")
)

// DiskName is a typed identifier for configured disks.
type DiskName string

// Config defines all configured disks and the global rclone configuration path.
type Config struct {
	Default DiskName
	Disks   map[DiskName]DiskConfig

	// RcloneConfigPath is process-scoped. All rclone-backed disks share this path.
	// RcloneConfigData is an inline config (ini content) kept in memory (no temp file).
	// Only one of RcloneConfigPath or RcloneConfigData may be set, and the first init wins for the process.
	RcloneConfigPath string
	RcloneConfigData string
}

// DiskConfig describes a single disk.
type DiskConfig struct {
	Driver string

	// rclone-specific (only used by rclone driver)
	Remote string
	Prefix string
}

// NormalizePath cleans a user path and rejects attempts to escape the disk root.
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
func JoinPrefix(prefix, p string) string {
	if prefix == "" {
		return p
	}
	if p == "" {
		return prefix
	}
	return path.Join(prefix, p)
}
