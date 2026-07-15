package s3storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	"github.com/aws/smithy-go/transport/http"

	"github.com/goforj/storage/storagecore"
)

// init registers the S3 driver with storagecore's runtime registry.
func init() {
	storagecore.RegisterDriver("s3", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client  s3API
	presign s3PresignAPI
	bucket  string
	prefix  string
}

type s3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type s3PresignAPI interface {
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

var buildS3Clients = func(cfg aws.Config, resolved storagecore.ResolvedConfig) (s3API, s3PresignAPI) {
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = resolved.S3UsePathStyle
		if resolved.S3UnsignedPayload {
			o.APIOptions = append(o.APIOptions, useUnsignedPayload)
		}
	})
	return client, s3.NewPresignClient(client)
}

// useUnsignedPayload swaps hashing for the S3-compatible unsigned payload sentinel when supported by the operation.
func useUnsignedPayload(stack *middleware.Stack) error {
	compute := &v4.ComputePayloadSHA256{}
	if _, ok := stack.Finalize.Get(compute.ID()); !ok {
		return nil
	}
	return v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware(stack)
}

// Config defines an S3-backed storage disk.
// @group Driver Config
//
// Example: define s3 storage config
//
//	cfg := s3storage.Config{
//		Bucket: "uploads",
//		Region: "us-east-1",
//	}
//	_ = cfg
//
// Example: define s3 storage config with all fields
//
//	cfg := s3storage.Config{
//		Bucket:          "uploads",
//		Endpoint:        "http://localhost:9000", // default: ""
//		Region:          "us-east-1",
//		AccessKeyID:     "minioadmin", // default: ""
//		SecretAccessKey: "minioadmin", // default: ""
//		UsePathStyle:    true,         // default: false
//		UnsignedPayload: false,        // default: false
//		Prefix:          "assets",     // default: ""
//	}
//	_ = cfg
type Config struct {
	Bucket          string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
	UnsignedPayload bool
	Prefix          string
}

// DriverName returns the registry name for S3 storage.
func (Config) DriverName() string { return "s3" }

// ResolvedConfig translates Config into the shared driver boundary.
func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
		Driver:            "s3",
		S3Bucket:          c.Bucket,
		S3Endpoint:        c.Endpoint,
		S3Region:          c.Region,
		S3AccessKeyID:     c.AccessKeyID,
		S3SecretAccessKey: c.SecretAccessKey,
		S3UsePathStyle:    c.UsePathStyle,
		S3UnsignedPayload: c.UnsignedPayload,
		Prefix:            c.Prefix,
	}
}

// New constructs S3-backed storage using AWS SDK v2.
// @group Driver Constructors
//
// Example: s3 storage
//
//	fs, _ := s3storage.New(s3storage.Config{
//		Bucket: "uploads",
//		Region: "us-east-1",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

// NewContext constructs S3-backed storage and honors cancellation during setup.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig validates the namespace and connection settings before constructing AWS clients.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.S3Bucket == "" {
		return nil, fmt.Errorf("storage: s3 storage requires S3Bucket")
	}
	if cfg.S3Region == "" {
		return nil, fmt.Errorf("storage: s3 storage requires S3Region")
	}
	if (cfg.S3AccessKeyID == "") != (cfg.S3SecretAccessKey == "") {
		return nil, fmt.Errorf("storage: s3 access key ID and secret access key must be provided together")
	}
	if cfg.S3Endpoint != "" {
		if _, err := normalizeEndpoint(cfg.S3Endpoint); err != nil {
			return nil, err
		}
	}

	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("storage: load aws config: %w", err)
	}
	client, presign := buildS3Clients(awsCfg, cfg)

	return &driver{
		client:  client,
		presign: presign,
		bucket:  cfg.S3Bucket,
		prefix:  prefix,
	}, nil
}

// loadAWSConfig builds an AWS configuration without changing process-global SDK state.
func loadAWSConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (aws.Config, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return aws.Config{}, err
	}
	optFns := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.S3Region),
	}
	if cfg.S3Endpoint != "" {
		endpoint, err := normalizeEndpoint(cfg.S3Endpoint)
		if err != nil {
			return aws.Config{}, err
		}
		optFns = append(optFns, config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               endpoint,
					HostnameImmutable: true,
				}, nil
			}),
		))
	}
	if cfg.S3AccessKeyID != "" || cfg.S3SecretAccessKey != "" {
		optFns = append(optFns, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, "")))
	}
	return config.LoadDefaultConfig(ctx, optFns...)
}

// Get implements storagecore.Storage using a background context.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext reads an object while observing cancellation and always closes the response body.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, fmt.Errorf("%w: a file path is required", storagecore.ErrForbidden)
	}
	key, err := d.key(normalized)
	if err != nil {
		return nil, err
	}
	out, err := d.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, wrapError(err)
	}
	var data bytes.Buffer
	_, err = copyContext(ctx, &data, out.Body)
	closeErr := out.Body.Close()
	if err != nil {
		return nil, joinCleanup(wrapError(err), wrapError(closeErr))
	}
	if closeErr != nil {
		return nil, wrapError(closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

// Put implements storagecore.Storage using a background context.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir implements storagecore.Storage using a background context.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext replaces an object at a non-root logical path.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	if normalized == "" {
		return fmt.Errorf("%w: a file path is required", storagecore.ErrForbidden)
	}
	key, err := d.key(normalized)
	if err != nil {
		return err
	}
	_, err = d.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(d.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(contents),
		ContentLength: aws.Int64(int64(len(contents))),
	})
	if err != nil {
		return wrapError(err)
	}
	return nil
}

// MakeDirContext creates an empty S3 directory marker and treats the logical root as already present.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	if normalized == "" {
		return nil
	}
	key, err := d.key(normalized)
	if err != nil {
		return err
	}
	_, err = d.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(d.bucket),
		Key:           aws.String(key + "/"),
		Body:          bytes.NewReader(nil),
		ContentLength: aws.Int64(0),
	})
	if err != nil {
		return wrapError(err)
	}
	return nil
}

// Delete implements storagecore.Storage using a background context.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext removes a file or an empty directory marker without recursively deleting children.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	if normalized == "" {
		return fmt.Errorf("%w: deleting the S3 root is not allowed", storagecore.ErrForbidden)
	}
	key, err := d.key(normalized)
	if err != nil {
		return err
	}
	entry, err := d.StatContext(ctx, p)
	if err != nil {
		return err
	}
	if entry.IsDir {
		out, err := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(d.bucket),
			Prefix:  aws.String(key + "/"),
			MaxKeys: aws.Int32(2),
		})
		if err != nil {
			return wrapError(err)
		}
		for _, object := range out.Contents {
			if aws.ToString(object.Key) != key+"/" {
				return fmt.Errorf("%w: directory not empty", storagecore.ErrForbidden)
			}
		}
		if len(out.CommonPrefixes) > 0 || aws.ToBool(out.IsTruncated) {
			return fmt.Errorf("%w: directory not empty", storagecore.ErrForbidden)
		}
		key += "/"
	}
	_, err = d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return wrapError(err)
	}
	return nil
}

// Stat implements storagecore.Storage using a background context.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext resolves files, explicit directory markers, and implicit S3 directories.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if normalized == "" {
		return storagecore.Entry{Path: "", IsDir: true}, nil
	}
	key, err := d.key(normalized)
	if err != nil {
		return storagecore.Entry{}, err
	}
	out, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return storagecore.Entry{Path: d.stripPrefix(key), Size: aws.ToInt64(out.ContentLength), IsDir: false}, nil
	}
	if !isNotFound(err) {
		return storagecore.Entry{}, wrapError(err)
	}
	if _, dirErr := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key + "/"),
	}); dirErr == nil {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	} else if !isNotFound(dirErr) {
		return storagecore.Entry{}, wrapError(dirErr)
	}
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	listOut, listErr := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(d.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	if listErr != nil {
		return storagecore.Entry{}, wrapError(listErr)
	}
	if len(listOut.Contents) > 0 || len(listOut.CommonPrefixes) > 0 {
		return storagecore.Entry{Path: d.stripPrefix(key), IsDir: true}, nil
	}
	return storagecore.Entry{}, wrapError(err)
}

// Exists implements storagecore.Storage using a background context.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext reports only file objects because directory presence is exposed through Stat.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return false, err
	}
	if normalized == "" {
		return false, nil
	}
	key, err := d.key(normalized)
	if err != nil {
		return false, err
	}
	_, err = d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, wrapError(err)
	}
	return true, nil
}

// List implements storagecore.Storage using a background context.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage implements storagecore.PagedStorage using a background context.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext returns immediate children in deterministic path order across all S3 pages.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix, err := d.key(p)
	if err != nil {
		return nil, err
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	entryByPath := make(map[string]storagecore.Entry)
	var token *string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(d.bucket),
			Prefix:            aws.String(prefix),
			Delimiter:         aws.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, wrapError(err)
		}
		for _, cp := range out.CommonPrefixes {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rel := strings.TrimSuffix(d.stripPrefix(aws.ToString(cp.Prefix)), "/")
			if rel == "" {
				continue
			}
			addDirectoryEntry(entryByPath, rel)
		}
		for _, obj := range out.Contents {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if strings.HasSuffix(aws.ToString(obj.Key), "/") {
				continue
			}
			rel := d.stripPrefix(aws.ToString(obj.Key))
			if rel == "" {
				continue
			}
			entryByPath[rel] = storagecore.Entry{
				Path:  rel,
				Size:  aws.ToInt64(obj.Size),
				IsDir: false,
			}
		}
		if aws.ToBool(out.IsTruncated) && out.NextContinuationToken != nil {
			token = out.NextContinuationToken
			continue
		}
		break
	}
	entries := make([]storagecore.Entry, 0, len(entryByPath))
	for _, entry := range entryByPath {
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

// ListPageContext paginates the driver's deterministic logical listing.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

// Walk implements storagecore.Storage using a background context.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext traverses files and synthesized directories in deterministic path order.
func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("%w: walk callback is required", storagecore.ErrForbidden)
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	prefix, err := d.key(normalized)
	if err != nil {
		return err
	}
	requestedPath := normalized
	if normalized != "" {
		if head, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(d.bucket),
			Key:    aws.String(prefix),
		}); err == nil {
			return fn(storagecore.Entry{Path: requestedPath, Size: aws.ToInt64(head.ContentLength)})
		} else if !isNotFound(err) {
			return wrapError(err)
		}
	}
	if prefix != "" {
		prefix += "/"
	}

	entryByPath := map[string]storagecore.Entry{}
	found := normalized == ""
	var token *string
	for {
		out, err := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(d.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return wrapError(err)
		}
		for _, obj := range out.Contents {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := aws.ToString(obj.Key)
			if key != "" {
				found = true
			}
			if strings.HasSuffix(key, "/") {
				dir := strings.TrimSuffix(d.stripPrefix(key), "/")
				if dir != "" && dir != requestedPath && (requestedPath == "" || strings.HasPrefix(dir, requestedPath+"/")) {
					addDirectoryEntry(entryByPath, dir)
					for _, parent := range recursiveParentDirs(dir + "/marker") {
						if parent != requestedPath && (requestedPath == "" || strings.HasPrefix(parent, requestedPath+"/")) {
							addDirectoryEntry(entryByPath, parent)
						}
					}
				}
				continue
			}
			rel := d.stripPrefix(key)
			if rel == "" {
				continue
			}
			for _, dir := range recursiveParentDirs(rel) {
				if dir == requestedPath || (requestedPath != "" && !strings.HasPrefix(dir, requestedPath+"/")) {
					continue
				}
				addDirectoryEntry(entryByPath, dir)
			}
			entryByPath[rel] = storagecore.Entry{
				Path:  rel,
				Size:  aws.ToInt64(obj.Size),
				IsDir: false,
			}
		}
		if aws.ToBool(out.IsTruncated) && out.NextContinuationToken != nil {
			token = out.NextContinuationToken
			continue
		}
		break
	}
	if !found {
		return fmt.Errorf("%w: object not found", storagecore.ErrNotFound)
	}
	entries := make([]storagecore.Entry, 0, len(entryByPath))
	for _, entry := range entryByPath {
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return nil
}

// Copy implements storagecore.Storage using a background context.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext copies a file through the storage contract and treats identical paths as a no-op.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalizedSrc, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	normalizedDst, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if normalizedSrc == "" || normalizedDst == "" {
		return fmt.Errorf("%w: copy requires non-root paths", storagecore.ErrForbidden)
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	if normalizedSrc == normalizedDst {
		return nil
	}
	return d.PutContext(ctx, dst, data)
}

// Move implements storagecore.Storage using a background context.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext moves files or directory trees without deleting identical source paths.
func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalizedSrc, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	normalizedDst, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if normalizedSrc == "" || normalizedDst == "" {
		return fmt.Errorf("%w: move requires non-root paths", storagecore.ErrForbidden)
	}
	srcEntry, err := d.StatContext(ctx, src)
	if err != nil {
		return err
	}
	if normalizedSrc == normalizedDst {
		return nil
	}
	if srcEntry.IsDir {
		return storagecore.MoveDirContext(ctx, d, src, dst)
	}
	if err := d.CopyContext(ctx, src, dst); err != nil {
		return err
	}
	return d.DeleteContext(ctx, src)
}

// URL implements storagecore.Storage using a background context.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext returns a presigned file URL with a bounded lifetime.
func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", fmt.Errorf("%w: a file path is required", storagecore.ErrForbidden)
	}
	key, err := d.key(normalized)
	if err != nil {
		return "", err
	}
	out, err := d.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(15*time.Minute))
	if err != nil {
		return "", wrapError(err)
	}
	return out.URL, nil
}

// key resolves a caller path beneath the configured S3 prefix.
func (d *driver) key(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// stripPrefix converts an S3 key back to a logical storage path.
func (d *driver) stripPrefix(k string) string {
	if d.prefix == "" {
		return k
	}
	if k == d.prefix {
		return ""
	}
	return strings.TrimPrefix(k, d.prefix+"/")
}

// recursiveParentDirs derives every directory implied by an object path.
func recursiveParentDirs(p string) []string {
	dir := path.Dir(p)
	if dir == "." || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// addDirectoryEntry preserves an explicit file when S3 contains colliding file and directory keys.
func addDirectoryEntry(entries map[string]storagecore.Entry, path string) {
	if existing, ok := entries[path]; ok && !existing.IsDir {
		return
	}
	entries[path] = storagecore.Entry{Path: path, IsDir: true}
}

// wrapError maps modeled and HTTP-level AWS errors to storagecore sentinels.
func wrapError(err error) error {
	if err == nil {
		return nil
	}
	var nfe *types.NotFound
	if errors.As(err, &nfe) {
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(apiErr.ErrorCode()) {
		case "nosuchbucket", "nosuchkey", "notfound":
			return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
		case "accessdenied", "forbidden", "unauthorized", "invalidaccesskeyid", "signaturedoesnotmatch":
			return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
		}
	}
	var responseErr *http.ResponseError
	if errors.As(err, &responseErr) {
		switch responseErr.HTTPStatusCode() {
		case 404:
			return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
		case 401, 403:
			return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
		}
	}
	return err
}

// normalizeEndpoint accepts only absolute HTTP endpoints safe for the AWS resolver.
func normalizeEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("storage: s3 endpoint must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("storage: s3 endpoint must not contain user info, a query, or a fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// copyContext stops stream processing promptly when the caller cancels.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

// joinCleanup preserves a primary failure while retaining response cleanup errors.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// isNotFound recognizes every not-found shape handled by wrapError.
func isNotFound(err error) bool {
	return errors.Is(wrapError(err), storagecore.ErrNotFound)
}
