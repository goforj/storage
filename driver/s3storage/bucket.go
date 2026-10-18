package s3storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/goforj/storage/storagecore"
)

// bucketAPI is separate from ordinary object operations because provisioning is explicitly requested.
type bucketAPI interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

// PrepareBucketContext creates a missing bucket using the same configuration and credentials as the driver.
// Existing buckets are unchanged. Permission failures never trigger a creation attempt.
func PrepareBucketContext(ctx context.Context, cfg Config) error {
	ctx = normalizeContext(ctx)
	store, err := newFromDiskConfig(ctx, cfg.ResolvedConfig())
	if err != nil {
		return err
	}
	client, ok := store.(*driver).client.(bucketAPI)
	if !ok {
		return storagecore.ErrUnsupported
	}
	if _, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)}); err == nil {
		return nil
	}
	var api smithy.APIError
	if !errors.As(err, &api) || api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchBucket" {
		return fmt.Errorf("storage: inspect bucket: %w", wrapError(err))
	}
	input := &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)}
	if cfg.Region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(cfg.Region)}
	}
	_, err = client.CreateBucket(ctx, input)
	if err != nil {
		return fmt.Errorf("storage: prepare bucket: %w", wrapError(err))
	}
	return nil
}
