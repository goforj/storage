package storagecore

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

type Storage interface {
	Get(p string) ([]byte, error)
	Put(p string, contents []byte) error
	MakeDir(p string) error
	Delete(p string) error
	Stat(p string) (Entry, error)
	Exists(p string) (bool, error)
	List(p string) ([]Entry, error)
	Walk(p string, fn func(Entry) error) error
	Copy(src, dst string) error
	Move(src, dst string) error
	URL(p string) (string, error)
}

type ContextStorage interface {
	GetContext(ctx context.Context, p string) ([]byte, error)
	PutContext(ctx context.Context, p string, contents []byte) error
	MakeDirContext(ctx context.Context, p string) error
	DeleteContext(ctx context.Context, p string) error
	StatContext(ctx context.Context, p string) (Entry, error)
	ExistsContext(ctx context.Context, p string) (bool, error)
	ListContext(ctx context.Context, p string) ([]Entry, error)
	WalkContext(ctx context.Context, p string, fn func(Entry) error) error
	CopyContext(ctx context.Context, src, dst string) error
	MoveContext(ctx context.Context, src, dst string) error
	URLContext(ctx context.Context, p string) (string, error)
}

type ListPageResult struct {
	Entries []Entry
	Offset  int
	Limit   int
	HasMore bool
}

type PagedStorage interface {
	ListPage(p string, offset, limit int) (ListPageResult, error)
}

type ContextPagedStorage interface {
	ListPageContext(ctx context.Context, p string, offset, limit int) (ListPageResult, error)
}

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

type DiskName string

type ResolvedConfig struct {
	Driver string

	Remote           string
	Prefix           string
	RcloneConfigPath string
	RcloneConfigData string

	S3Bucket          string
	S3Endpoint        string
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3UsePathStyle    bool
	S3UnsignedPayload bool

	SFTPHost                  string
	SFTPPort                  int
	SFTPUser                  string
	SFTPPassword              string
	SFTPKeyPath               string
	SFTPKnownHostsPath        string
	SFTPInsecureIgnoreHostKey bool

	FTPHost               string
	FTPPort               int
	FTPUser               string
	FTPPassword           string
	FTPTLS                bool
	FTPInsecureSkipVerify bool

	DropboxToken string

	GCSBucket          string
	GCSCredentialsJSON string
	GCSEndpoint        string

	RedisAddr     string
	RedisUsername string
	RedisPassword string
	RedisDB       int
}

func NormalizePath(p string) (string, error) {
	cleanedInput := strings.TrimSpace(p)
	cleanedInput = strings.ReplaceAll(cleanedInput, "\\", "/")
	cleaned := path.Clean(cleanedInput)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		cleaned = ""
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: invalid path", ErrForbidden)
	}
	return cleaned, nil
}

func JoinPrefix(prefix, p string) string {
	if prefix == "" {
		return p
	}
	if p == "" {
		return prefix
	}
	return path.Join(prefix, p)
}

func PaginateEntries(entries []Entry, offset, limit int) ListPageResult {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	pageEntries := make([]Entry, end-offset)
	copy(pageEntries, entries[offset:end])
	return ListPageResult{
		Entries: pageEntries,
		Offset:  offset,
		Limit:   limit,
		HasMore: end < len(entries),
	}
}
