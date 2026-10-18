package storagecore

import (
	"context"
	"io"
	"time"
)

// ObjectMetadata contains portable HTTP content attributes retained by object stores.
type ObjectMetadata struct {
	ContentType        string
	ContentEncoding    string
	ContentDisposition string
	CacheControl       string
	Values             map[string]string
}

// ObjectInfo describes an object using its exact key relative to the configured disk prefix.
// Keys are opaque, including dot segments and repeated slashes, and are never filesystem paths.
type ObjectInfo struct {
	Key        string
	Size       int64
	ETag       string
	Version    string
	ModifiedAt time.Time
	Metadata   ObjectMetadata
}

// OpenObjectOptions optionally pins a read to a version or a previously observed entity tag.
type OpenObjectOptions struct {
	Version string
	IfMatch string
}

// ObjectReader owns a streaming response that the caller must close, including after partial reads.
type ObjectReader struct {
	io.ReadCloser
	Info ObjectInfo
}

// PutObjectOptions declares the exact stream size and portable metadata without buffering the object.
// The caller owns the reader and should provide a seekable reader when retries are required.
type PutObjectOptions struct {
	Size     int64
	Metadata ObjectMetadata
}

// ObjectStorage is an optional streaming capability for opaque object keys.
// Existing Storage path semantics and buffered methods remain unchanged. Walk visits
// objects page by page without synthesizing directories or accumulating the whole listing.
type ObjectStorage interface {
	WalkObjectsContext(ctx context.Context, prefix string, visit func(ObjectInfo) error) error
	OpenObjectContext(ctx context.Context, key string, options OpenObjectOptions) (*ObjectReader, error)
	PutObjectContext(ctx context.Context, key string, body io.Reader, options PutObjectOptions) (ObjectInfo, error)
}
