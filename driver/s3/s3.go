package s3driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/goforj/filesystem"
)

func init() {
	filesystem.RegisterDriver("s3", New)
}

type Driver struct {
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

func New(ctx context.Context, cfg filesystem.DiskConfig, _ filesystem.Config) (filesystem.Filesystem, error) {
	if cfg.S3Bucket == "" {
		return nil, fmt.Errorf("filesystem: s3 driver requires S3Bucket")
	}
	if cfg.S3Region == "" {
		return nil, fmt.Errorf("filesystem: s3 driver requires S3Region")
	}

	prefix, err := filesystem.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}

	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("filesystem: load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.S3UsePathStyle
	})
	presign := s3.NewPresignClient(client)

	return &Driver{
		client:  client,
		presign: presign,
		bucket:  cfg.S3Bucket,
		prefix:  prefix,
	}, nil
}

func loadAWSConfig(ctx context.Context, cfg filesystem.DiskConfig) (aws.Config, error) {
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

func (d *Driver) Get(ctx context.Context, p string) ([]byte, error) {
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

func (d *Driver) Put(ctx context.Context, p string, contents []byte) error {
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

func (d *Driver) Delete(ctx context.Context, p string) error {
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

func (d *Driver) Exists(ctx context.Context, p string) (bool, error) {
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

func (d *Driver) List(ctx context.Context, p string) ([]filesystem.Entry, error) {
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

	var entries []filesystem.Entry
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
			entries = append(entries, filesystem.Entry{Path: rel, IsDir: true})
		}
		for _, obj := range out.Contents {
			if strings.HasSuffix(aws.ToString(obj.Key), "/") {
				continue
			}
			rel := d.stripPrefix(aws.ToString(obj.Key))
			if rel == "" {
				continue
			}
			entries = append(entries, filesystem.Entry{
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

func (d *Driver) URL(ctx context.Context, p string) (string, error) {
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

func (d *Driver) key(p string) (string, error) {
	normalized, err := filesystem.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return filesystem.JoinPrefix(d.prefix, normalized), nil
}

func (d *Driver) stripPrefix(k string) string {
	if d.prefix == "" {
		return k
	}
	trimmed := strings.TrimPrefix(k, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func wrapError(err error) error {
	var nfe *types.NotFound
	if errors.As(err, &nfe) {
		return fmt.Errorf("%w: %v", filesystem.ErrNotFound, err)
	}
	var apiErr *types.NoSuchKey
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %v", filesystem.ErrNotFound, err)
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
