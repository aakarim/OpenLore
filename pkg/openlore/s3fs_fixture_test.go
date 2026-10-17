//go:build integration

package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3FileClient is the object-storage boundary of S3FS. *s3.Client implements it.
type S3FileClient interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3FS is a test-only WritableFS for one docset rooted at /. File bytes
// are immutable content-addressed objects; a conditional manifest PUT publishes
// namespace changes, including whole-tree deletes, atomically. Separate clients
// cannot silently overwrite one another's manifest updates: contention returns
// an S3 conditional-write error, and callers must re-read before retrying.
//
// It reads/writes the whole manifest per operation and does not garbage-collect
// orphaned blobs or implement xattrs. Use a dedicated prefix, with no external
// writers. A failed PUT may have committed remotely; this is not a transaction
// with the commit journal. NewServerWithRootFS can mount this backend; journal,
// pre-image blobs and other server state still use the configured local data dir.
type S3FS struct {
	client   S3FileClient
	bucket   string
	prefix   string
	mu       sync.RWMutex
	writable bool
}

type s3FileEntry struct {
	Dir      bool      `json:"dir,omitempty"`
	Hash     string    `json:"hash,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modified"`
}

type s3FileManifest struct {
	Version int                    `json:"version"`
	Entries map[string]s3FileEntry `json:"entries"`
}

var _ vfs.WritableFS = (*S3FS)(nil)

// NewS3FS uses an existing bucket and caller-configured SDK client (credentials,
// endpoint, region, request timeouts). Construction performs no remote writes;
// the backend starts read-only. S3 conditional writes must be supported.
func NewS3FS(client S3FileClient, bucket, prefix string) (*S3FS, error) {
	if client == nil || bucket == "" {
		return nil, errors.New("S3 filesystem requires a client and bucket")
	}
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3FS{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *S3FS) SetWriteable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writable = true
	return nil
}

func (s *S3FS) SetReadonly() error {
	s.mu.Lock() // drains mutations holding RLock through their remote PUT
	defer s.mu.Unlock()
	s.writable = false
	return nil
}

func s3FilePath(name string) (string, error) {
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\\") || hasTraversal(name) || (hasReservedPath(name) && !isDirConfigPath(name)) {
		return "", fmt.Errorf("access denied: %s", name)
	}
	return vfs.CleanPath(name), nil
}

func (s *S3FS) manifest() (s3FileManifest, *string, error) {
	object, err := s.client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + "manifest.json")})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "NoSuchKey" {
			return s3FileManifest{Version: 1, Entries: map[string]s3FileEntry{"/": {Dir: true}}}, nil, nil
		}
		return s3FileManifest{}, nil, err
	}
	defer object.Body.Close()
	var manifest s3FileManifest
	if err := json.NewDecoder(object.Body).Decode(&manifest); err != nil {
		return manifest, nil, err
	}
	if manifest.Version != 1 || manifest.Entries == nil || !manifest.Entries["/"].Dir || aws.ToString(object.ETag) == "" {
		return manifest, nil, errors.New("invalid S3 filesystem manifest")
	}
	return manifest, object.ETag, nil
}

func (s *S3FS) mutate(fn func(map[string]s3FileEntry) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.writable {
		return vfs.ErrReadOnly
	}
	manifest, etag, err := s.manifest()
	if err != nil {
		return err
	}
	if err := fn(manifest.Entries); err != nil {
		return err
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + "manifest.json"), Body: bytes.NewReader(body), IfMatch: etag}
	if etag == nil {
		input.IfNoneMatch = aws.String("*")
	}
	_, err = s.client.PutObject(context.Background(), input)
	return err
}

func (s *S3FS) Stat(name string) (*vfs.FileInfo, error) {
	name, err := s3FilePath(name)
	if err != nil {
		return nil, err
	}
	manifest, _, err := s.manifest()
	if err != nil {
		return nil, err
	}
	entry, exists := manifest.Entries[name]
	if !exists {
		return nil, vfs.ErrNotFound(name)
	}
	return &vfs.FileInfo{FileName: path.Base(name), FilePath: name, Dir: entry.Dir, FileModTime: entry.Modified, FileSize: entry.Size}, nil
}

func (s *S3FS) ReadFile(name string) ([]byte, error) {
	name, err := s3FilePath(name)
	if err != nil {
		return nil, err
	}
	manifest, _, err := s.manifest()
	if err != nil {
		return nil, err
	}
	entry, exists := manifest.Entries[name]
	if !exists {
		return nil, vfs.ErrNotFound(name)
	}
	if entry.Dir {
		return nil, vfs.ErrIsDirectory(name)
	}
	object, err := s.client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + "objects/" + entry.Hash)})
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()
	body, err := io.ReadAll(object.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != entry.Size || hashContent(body) != entry.Hash {
		return nil, fmt.Errorf("S3 file integrity mismatch: %s", name)
	}
	return body, nil
}

func (s *S3FS) ReadDir(name string) ([]vfs.FileInfo, error) {
	name, err := s3FilePath(name)
	if err != nil {
		return nil, err
	}
	manifest, _, err := s.manifest()
	if err != nil {
		return nil, err
	}
	entry, exists := manifest.Entries[name]
	if !exists {
		return nil, vfs.ErrNotFound(name)
	}
	if !entry.Dir {
		return nil, vfs.ErrNotDirectory(name)
	}
	var entries []vfs.FileInfo
	for p, entry := range manifest.Entries {
		if p != name && path.Dir(p) == name {
			if _, err := s3FilePath(p); err != nil {
				continue
			}
			entries = append(entries, vfs.FileInfo{FileName: path.Base(p), FilePath: p, Dir: entry.Dir, FileModTime: entry.Modified, FileSize: entry.Size})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].FileName < entries[j].FileName })
	return entries, nil
}

func s3FileParents(entries map[string]s3FileEntry, name string) error {
	for parent := path.Dir(name); parent != "/"; parent = path.Dir(parent) {
		if entry, exists := entries[parent]; exists {
			if !entry.Dir {
				return vfs.ErrNotDirectory(parent)
			}
		} else {
			entries[parent] = s3FileEntry{Dir: true, Modified: time.Now().UTC()}
		}
	}
	return nil
}

func (s *S3FS) WriteFileAtomic(name string, body []byte, opts vfs.WriteOpts) (string, error) {
	name, err := s3FilePath(name)
	if err != nil {
		return "", err
	}
	if !opts.ContentPolicyValidated && len(body) > defaultMaxWriteBytes {
		return "", errors.New("S3 file exceeds write size limit")
	}
	hash := hashContent(body)
	err = s.mutate(func(entries map[string]s3FileEntry) error {
		current, exists := entries[name]
		if current.Dir {
			return vfs.ErrIsDirectory(name)
		}
		if opts.IfNoneMatch && exists || opts.IfMatch != nil && (!exists || current.Hash != *opts.IfMatch) {
			return &vfs.PreconditionError{Path: name, Current: current.Hash}
		}
		if err := s3FileParents(entries, name); err != nil {
			return err
		}
		if _, err := s.client.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + "objects/" + hash), Body: bytes.NewReader(body)}); err != nil {
			return err
		}
		entries[name] = s3FileEntry{Hash: hash, Size: int64(len(body)), Modified: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hash, nil
}

func (s *S3FS) Mkdir(name string) error    { return s.mkdir(name, false) }
func (s *S3FS) MkdirAll(name string) error { return s.mkdir(name, true) }

func (s *S3FS) mkdir(name string, parents bool) error {
	name, err := s3FilePath(name)
	if err != nil {
		return err
	}
	if name == "/" {
		return errors.New("cannot create docset root")
	}
	return s.mutate(func(entries map[string]s3FileEntry) error {
		if entry, exists := entries[name]; exists {
			if parents && entry.Dir {
				return nil
			}
			return os.ErrExist
		}
		if parents {
			if err := s3FileParents(entries, name); err != nil {
				return err
			}
		} else if parent, exists := entries[path.Dir(name)]; !exists {
			return vfs.ErrNotFound(path.Dir(name))
		} else if !parent.Dir {
			return vfs.ErrNotDirectory(path.Dir(name))
		}
		entries[name] = s3FileEntry{Dir: true, Modified: time.Now().UTC()}
		return nil
	})
}

func (s *S3FS) Remove(name string) error                         { return s.remove(name, false, vfs.RemoveOpts{}) }
func (s *S3FS) RemoveAll(name string, opts vfs.RemoveOpts) error { return s.remove(name, true, opts) }

func (s *S3FS) remove(name string, recursive bool, opts vfs.RemoveOpts) error {
	name, err := s3FilePath(name)
	if err != nil {
		return err
	}
	if name == "/" {
		return errors.New("cannot remove docset root")
	}
	return s.mutate(func(entries map[string]s3FileEntry) error {
		got := map[string]vfs.TreeOp{}
		for p, entry := range entries {
			if p == name || strings.HasPrefix(p, name+"/") {
				if _, err := s3FilePath(p); err != nil {
					return fmt.Errorf("cannot remove tree containing denied path: %s", p)
				}
				rel := strings.TrimPrefix(p, name+"/")
				if p == name {
					rel = "."
				}
				kind := "file"
				if entry.Dir {
					kind = "dir"
				}
				got[rel] = vfs.TreeOp{RelPath: rel, Kind: kind, Hash: entry.Hash, Size: entry.Size}
			}
		}
		if opts.Expected != nil {
			want := map[string]vfs.TreeOp{}
			for _, op := range opts.Expected.Ops {
				want[op.RelPath] = op
			}
			if opts.Expected.Root != name || len(want) != len(opts.Expected.Ops) || !maps.Equal(got, want) {
				return &vfs.TreeStaleError{Path: name, Detail: "S3 subtree differs from expected snapshot"}
			}
		}
		if len(got) == 0 {
			if recursive {
				return nil
			}
			return vfs.ErrNotFound(name)
		}
		if !recursive && len(got) > 1 {
			return fmt.Errorf("directory not empty: %s", name)
		}
		for p := range entries {
			if p == name || strings.HasPrefix(p, name+"/") {
				delete(entries, p)
			}
		}
		return nil
	})
}
