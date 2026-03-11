package s3storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/goforj/storage/storagecore"
)

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
	})
	return client, s3.NewPresignClient(client)
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

func (Config) DriverName() string { return "s3" }

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

func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	if cfg.S3Bucket == "" {
		return nil, fmt.Errorf("storage: s3 storage requires S3Bucket")
	}
	if cfg.S3Region == "" {
		return nil, fmt.Errorf("storage: s3 storage requires S3Region")
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

func loadAWSConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (aws.Config, error) {
	optFns := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.S3Region),
	}
	if cfg.S3Endpoint != "" {
		optFns = append(optFns, config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               cfg.S3Endpoint,
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

func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := d.key(p)
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
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
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

func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

func (d *driver) DeleteContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := d.key(p)
	if err != nil {
		return err
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

func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	key, err := d.key(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	out, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return storagecore.Entry{}, wrapError(err)
	}
	return storagecore.Entry{Path: d.stripPrefix(key), Size: aws.ToInt64(out.ContentLength), IsDir: false}, nil
}

func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := d.key(p)
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

func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
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

	var entries []storagecore.Entry
	var token *string
	for {
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
			rel := strings.TrimSuffix(d.stripPrefix(aws.ToString(cp.Prefix)), "/")
			if rel == "" {
				continue
			}
			entries = append(entries, storagecore.Entry{Path: rel, IsDir: true})
		}
		for _, obj := range out.Contents {
			if strings.HasSuffix(aws.ToString(obj.Key), "/") {
				continue
			}
			rel := d.stripPrefix(aws.ToString(obj.Key))
			if rel == "" {
				continue
			}
			entries = append(entries, storagecore.Entry{
				Path:  rel,
				Size:  aws.ToInt64(obj.Size),
				IsDir: false,
			})
		}
		if aws.ToBool(out.IsTruncated) && out.NextContinuationToken != nil {
			token = out.NextContinuationToken
			continue
		}
		break
	}
	return entries, nil
}

func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix, err := d.key(p)
	if err != nil {
		return err
	}
	fileExists := false
	if prefix != "" {
		if _, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(d.bucket),
			Key:    aws.String(prefix),
		}); err == nil {
			fileExists = true
		} else if !isNotFound(err) {
			return wrapError(err)
		}
		prefix += "/"
	}

	seenDirs := map[string]struct{}{}
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
			if strings.HasSuffix(key, "/") {
				continue
			}
			rel := d.stripPrefix(key)
			if rel == "" {
				continue
			}
			for _, dir := range recursiveParentDirs(rel) {
				if _, ok := seenDirs[dir]; ok {
					continue
				}
				seenDirs[dir] = struct{}{}
				if err := fn(storagecore.Entry{Path: dir, IsDir: true}); err != nil {
					return err
				}
			}
			if err := fn(storagecore.Entry{
				Path:  rel,
				Size:  aws.ToInt64(obj.Size),
				IsDir: false,
			}); err != nil {
				return err
			}
		}
		if aws.ToBool(out.IsTruncated) && out.NextContinuationToken != nil {
			token = out.NextContinuationToken
			continue
		}
		break
	}
	if fileExists {
		return fn(storagecore.Entry{Path: d.stripPrefix(strings.TrimSuffix(prefix, "/")), IsDir: false})
	}
	return nil
}

func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	return d.PutContext(ctx, dst, data)
}

func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	if err := d.CopyContext(ctx, src, dst); err != nil {
		return err
	}
	return d.DeleteContext(ctx, src)
}

func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key, err := d.key(p)
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

func (d *driver) key(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) stripPrefix(k string) string {
	if d.prefix == "" {
		return k
	}
	trimmed := strings.TrimPrefix(k, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

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

func wrapError(err error) error {
	var nfe *types.NotFound
	if errors.As(err, &nfe) {
		return fmt.Errorf("%w: %v", storagecore.ErrNotFound, err)
	}
	var apiErr *types.NoSuchKey
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %v", storagecore.ErrNotFound, err)
	}
	return err
}

func isNotFound(err error) bool {
	var nfe *types.NotFound
	if errors.As(err, &nfe) {
		return true
	}
	var apiErr *types.NoSuchKey
	return errors.As(err, &apiErr)
}
