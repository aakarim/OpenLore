//go:build integration

package analytics

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3EventStore is a test-only adapter for shipped events in an S3 bucket prefix.
// The caller supplies a client with credentials, region and endpoint configured;
// constructing a store does not create a bucket or change its policies.
type S3EventStore struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3EventStore(client *s3.Client, bucket, prefix string) (*S3EventStore, error) {
	if client == nil || bucket == "" {
		return nil, fmt.Errorf("S3 analytics requires a client and bucket")
	}
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3EventStore{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *S3EventStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key), Body: body, ContentLength: aws.Int64(size),
	}, s3.WithAPIOptions(v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware))
	return err
}

func (s *S3EventStore) List(ctx context.Context, prefix string) ([]string, error) {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket), Prefix: aws.String(s.prefix + prefix),
	})
	var keys []string
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			keys = append(keys, strings.TrimPrefix(aws.ToString(object.Key), s.prefix))
		}
	}
	return keys, nil
}

func (s *S3EventStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key)})
	if err != nil {
		return nil, err
	}
	return object.Body, nil
}
