package s3storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/goforj/storage/storagecore"
)

// objectKey preserves opaque keys while keeping all access within the configured prefix.
func (d *driver) objectKey(key string) (string, error) {
	if strings.ContainsRune(key, '\x00') || d.prefix == "" && key == "" {
		return "", fmt.Errorf("%w: invalid object key", storagecore.ErrForbidden)
	}
	if d.prefix != "" {
		return d.prefix + "/" + key, nil
	}
	return key, nil
}

// objectPrefix scopes flat listings without treating keys as filesystem paths.
func (d *driver) objectPrefix(prefix string) (string, error) {
	if strings.ContainsRune(prefix, '\x00') {
		return "", fmt.Errorf("%w: invalid object prefix", storagecore.ErrForbidden)
	}
	if d.prefix != "" {
		return d.prefix + "/" + prefix, nil
	}
	return prefix, nil
}

// WalkObjectsContext iterates raw objects one provider page at a time and propagates callback cancellation.
func (d *driver) WalkObjectsContext(ctx context.Context, prefix string, visit func(storagecore.ObjectInfo) error) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if visit == nil {
		return fmt.Errorf("storage: object visitor is required")
	}
	scoped, err := d.objectPrefix(prefix)
	if err != nil {
		return err
	}
	var token *string
	for {
		page, err := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(d.bucket), Prefix: aws.String(scoped), ContinuationToken: token})
		if err != nil {
			return wrapError(err)
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			if !strings.HasPrefix(key, scoped) {
				return fmt.Errorf("%w: object outside disk prefix", storagecore.ErrForbidden)
			}
			relative := key
			if d.prefix != "" {
				relative = strings.TrimPrefix(key, d.prefix+"/")
			}
			info := storagecore.ObjectInfo{Key: relative, Size: aws.ToInt64(object.Size), ETag: aws.ToString(object.ETag)}
			if object.LastModified != nil {
				info.ModifiedAt = *object.LastModified
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(info); err != nil {
				return err
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil
		}
		next := aws.ToString(page.NextContinuationToken)
		if next == "" || next == aws.ToString(token) {
			return fmt.Errorf("storage: object listing did not advance")
		}
		token = page.NextContinuationToken
	}
}

// OpenObjectContext streams a prefix-scoped object with optional version and entity-tag conditions.
func (d *driver) OpenObjectContext(ctx context.Context, key string, options storagecore.OpenObjectOptions) (*storagecore.ObjectReader, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scoped, err := d.objectKey(key)
	if err != nil {
		return nil, err
	}
	input := &s3.GetObjectInput{Bucket: aws.String(d.bucket), Key: aws.String(scoped)}
	if options.Version != "" {
		input.VersionId = aws.String(options.Version)
	}
	if options.IfMatch != "" {
		input.IfMatch = aws.String(options.IfMatch)
	}
	out, err := d.client.GetObject(ctx, input)
	if err != nil {
		return nil, wrapError(err)
	}
	if options.IfMatch != "" && options.IfMatch != "*" && strings.Trim(options.IfMatch, "\"") != strings.Trim(aws.ToString(out.ETag), "\"") {
		out.Body.Close()
		return nil, fmt.Errorf("storage: object changed during conditional read")
	}
	info := storagecore.ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ETag: aws.ToString(out.ETag), Version: aws.ToString(out.VersionId), Metadata: storagecore.ObjectMetadata{ContentType: aws.ToString(out.ContentType), ContentEncoding: aws.ToString(out.ContentEncoding), ContentDisposition: aws.ToString(out.ContentDisposition), CacheControl: aws.ToString(out.CacheControl), Values: out.Metadata}}
	if out.LastModified != nil {
		info.ModifiedAt = *out.LastModified
	}
	return &storagecore.ObjectReader{ReadCloser: out.Body, Info: info}, nil
}

// PutObjectContext passes a bounded stream and its metadata directly to the provider.
func (d *driver) PutObjectContext(ctx context.Context, key string, body io.Reader, options storagecore.PutObjectOptions) (storagecore.ObjectInfo, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return storagecore.ObjectInfo{}, err
	}
	scoped, err := d.objectKey(key)
	if err != nil {
		return storagecore.ObjectInfo{}, err
	}
	if body == nil || options.Size < 0 {
		return storagecore.ObjectInfo{}, fmt.Errorf("storage: object body and nonnegative size are required")
	}
	metadata := options.Metadata
	input := &s3.PutObjectInput{Bucket: aws.String(d.bucket), Key: aws.String(scoped), Body: body, ContentLength: aws.Int64(options.Size), Metadata: metadata.Values}
	if metadata.ContentType != "" {
		input.ContentType = aws.String(metadata.ContentType)
	}
	if metadata.ContentEncoding != "" {
		input.ContentEncoding = aws.String(metadata.ContentEncoding)
	}
	if metadata.ContentDisposition != "" {
		input.ContentDisposition = aws.String(metadata.ContentDisposition)
	}
	if metadata.CacheControl != "" {
		input.CacheControl = aws.String(metadata.CacheControl)
	}
	out, err := d.client.PutObject(ctx, input)
	if err != nil {
		return storagecore.ObjectInfo{}, wrapError(err)
	}
	return storagecore.ObjectInfo{Key: key, Size: options.Size, ETag: aws.ToString(out.ETag), Version: aws.ToString(out.VersionId), Metadata: metadata}, nil
}
