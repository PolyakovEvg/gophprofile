package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const cacheControl = "public, max-age=31536000, immutable"

type s3Storage struct {
	client *s3.Client
	bucket string
	prefix string
}

// S3StorageConfig stores settings needed to create an S3 storage provider.
type S3StorageConfig struct {
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
}

// NewS3Storage creates an S3-backed avatar storage provider.
func NewS3Storage(
	client *s3.Client,
	bucket string,
	prefix string,
) Provider {
	return &s3Storage{
		client: client,
		bucket: bucket,
		prefix: prefix,
	}
}

// NewS3StorageFromConfig creates an S3-backed provider from storage settings.
func NewS3StorageFromConfig(cfg S3StorageConfig) Provider {
	awsCfg := aws.Config{
		Region: cfg.Region,
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		awsCfg.Credentials = aws.CredentialsProviderFunc(
			func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     cfg.AccessKey,
					SecretAccessKey: cfg.SecretKey,
				}, nil
			},
		)
	}

	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.Endpoint == "" {
			return
		}

		options.BaseEndpoint = aws.String(cfg.Endpoint)
		options.UsePathStyle = true
	})

	return NewS3Storage(client, cfg.Bucket, cfg.Endpoint)
}

func (s *s3Storage) Store(
	ctx context.Context,
	input StoreInput,
) (string, error) {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(input.Key),
		Body:         bytes.NewReader(input.Body),
		CacheControl: aws.String(cacheControl),
		ContentType:  aws.String(input.ContentType),
		Metadata: map[string]string{
			"user_id":   input.UserID.String(),
			"avatar_id": input.AvatarID.String(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload avatar to S3: %w", err)
	}

	targetURL, err := url.JoinPath(s.prefix, input.Key)
	if err != nil {
		return "", fmt.Errorf("failed to construct target URL: %w", err)
	}

	return targetURL, nil
}

func (s *s3Storage) Retrieve(ctx context.Context, key string) ([]byte, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve avatar from S3: %w", err)
	}
	defer result.Body.Close()

	payload, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to copy avatar body: %w", err)
	}

	return payload, nil
}

func (s *s3Storage) Delete(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if key == "" {
			continue
		}

		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("failed to delete avatar from S3: %w", err)
		}
	}

	return nil
}
