package analytics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/klauspost/compress/zstd"
)

type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string
	PathStyle bool
}

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type S3EventStore struct {
	client         s3API
	bucket, prefix string
}

// OpenS3EventStore uses the standard AWS credential chain (environment,
// shared config, web identity and instance/task roles).
func OpenS3EventStore(ctx context.Context, cfg S3Config) (*S3EventStore, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("analytics S3 bucket is required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.PathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return newS3EventStore(client, cfg), nil
}

func newS3EventStore(client s3API, cfg S3Config) *S3EventStore {
	return &S3EventStore{client: client, bucket: cfg.Bucket, prefix: strings.Trim(cfg.Prefix, "/")}
}
func (s *S3EventStore) key(key string) string {
	if s.prefix == "" {
		return strings.TrimLeft(key, "/")
	}
	return s.prefix + "/" + strings.TrimLeft(key, "/")
}
func (s *S3EventStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.key(key)), Body: body, ContentLength: aws.Int64(size)})
	return err
}
func (s *S3EventStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(s.key(key))})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}
func (s *S3EventStore) List(ctx context.Context, prefix string) ([]string, error) {
	fullPrefix := s.key(prefix)
	var keys []string
	var token *string
	for {
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(fullPrefix), ContinuationToken: token})
		if err != nil {
			return nil, err
		}
		for _, obj := range out.Contents {
			key := aws.ToString(obj.Key)
			if s.prefix != "" {
				key = strings.TrimPrefix(key, s.prefix+"/")
			}
			keys = append(keys, key)
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys)
	return keys, nil
}

type remoteSource struct {
	store  RemoteEventStore
	prefix string
}

// NewRemoteSource adapts shipped immutable JSONL ranges and sealed JSONL/zstd
// segments to EventSource.
func NewRemoteSource(store RemoteEventStore, prefixes ...string) EventSource {
	prefix := "events"
	if len(prefixes) > 0 {
		prefix = prefixes[0]
	}
	return &remoteSource{store: store, prefix: strings.Trim(prefix, "/")}
}

func RemoteSource(store RemoteEventStore) EventSource { return NewRemoteSource(store) }

func (s *remoteSource) Scan(ctx context.Context, filter EventFilter, fn func(Event) error) error {
	keys, err := s.store.List(ctx, s.prefix)
	if err != nil {
		return err
	}
	sort.Strings(keys)
	seen := map[string]struct{}{}
	for _, key := range keys {
		if !(strings.HasSuffix(key, ".jsonl") || strings.HasSuffix(key, ".jsonl.zst")) {
			continue
		}
		r, err := s.store.Get(ctx, key)
		if err != nil {
			return err
		}
		var input io.Reader = r
		var dec *zstd.Decoder
		if strings.HasSuffix(key, ".zst") {
			dec, err = zstd.NewReader(r)
			if err != nil {
				r.Close()
				return fmt.Errorf("decode %s: %w", key, err)
			}
			input = dec
		}
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				if dec != nil {
					dec.Close()
				}
				_ = r.Close()
				return err
			}
			var event Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				if dec != nil {
					dec.Close()
				}
				r.Close()
				return fmt.Errorf("decode %s: %w", key, err)
			}
			identity := event.ID
			if identity == "" {
				identity = string(scanner.Bytes())
			}
			if _, ok := seen[identity]; ok {
				continue
			}
			seen[identity] = struct{}{}
			if eventMatches(event, filter) {
				if err := fn(event); err != nil {
					if dec != nil {
						dec.Close()
					}
					_ = r.Close()
					return err
				}
			}
		}
		scanErr := scanner.Err()
		if dec != nil {
			dec.Close()
		}
		closeErr := r.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func eventMatches(e Event, f EventFilter) bool {
	if !f.From.IsZero() && e.Time.Before(f.From) || !f.To.IsZero() && e.Time.After(f.To) {
		return false
	}
	if len(f.Types) > 0 {
		found := false
		for _, v := range f.Types {
			if e.Type == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(f.Principals) > 0 {
		found := false
		for _, v := range f.Principals {
			if e.Principal == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
