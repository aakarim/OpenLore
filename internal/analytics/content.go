package analytics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type ContentScalarProvider interface {
	Name() string
	Scalars(string, []byte) map[string]float64
}
type Tokenizer interface {
	Name() string
	Count([]byte) int
}
type approxTokenizer struct{}

func (approxTokenizer) Name() string { return "approx" }
func (approxTokenizer) Count(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	return (len(b) + 3) / 4
}
func ApproxTokenizer() Tokenizer { return approxTokenizer{} }

type sizeProvider struct{}

func (sizeProvider) Name() string { return "size" }
func (sizeProvider) Scalars(_ string, b []byte) map[string]float64 {
	lines := 0
	if len(b) > 0 {
		lines = bytes.Count(b, []byte{'\n'})
		if b[len(b)-1] != '\n' {
			lines++
		}
	}
	return map[string]float64{"bytes": float64(len(b)), "lines": float64(lines), "words": float64(len(bytes.Fields(b)))}
}

type tokenProvider struct{ t Tokenizer }

func (p tokenProvider) Name() string          { return "tokens" }
func (p tokenProvider) tokenizerName() string { return p.t.Name() }
func (p tokenProvider) Scalars(_ string, b []byte) map[string]float64 {
	return map[string]float64{"tokens": float64(p.t.Count(b))}
}

var defaultProviders = []ContentScalarProvider{sizeProvider{}, tokenProvider{ApproxTokenizer()}}

func RegisterScalarProvider(p ContentScalarProvider) { defaultProviders = append(defaultProviders, p) }

type DocScalars struct {
	Path        string             `json:"path"`
	ContentHash string             `json:"content_hash"`
	Scalars     map[string]float64 `json:"scalars"`
	Tokenizer   string             `json:"tokenizer"`
	ComputedAt  time.Time          `json:"computed_at"`
}

func ComputeScalars(path string, content []byte) DocScalars {
	return computeScalars(path, content, defaultProviders)
}
func computeScalars(p string, b []byte, providers []ContentScalarProvider) DocScalars {
	h := sha256.Sum256(b)
	d := DocScalars{Path: vfs.CleanPath(p), ContentHash: hex.EncodeToString(h[:]), Scalars: map[string]float64{}, Tokenizer: tokenizerName(providers), ComputedAt: time.Now().UTC()}
	for _, provider := range providers {
		for k, v := range provider.Scalars(p, b) {
			if reservedScalar(k) && !builtinScalarProvider(provider, k) {
				continue
			}
			d.Scalars[k] = v
		}
	}
	return d
}

func tokenizerName(providers []ContentScalarProvider) string {
	for i := len(providers) - 1; i >= 0; i-- {
		if provider, ok := providers[i].(interface{ tokenizerName() string }); ok {
			return provider.tokenizerName()
		}
	}
	return "approx"
}

func reservedScalar(name string) bool {
	return name == "bytes" || name == "lines" || name == "words" || name == "tokens"
}

func builtinScalarProvider(provider ContentScalarProvider, scalar string) bool {
	switch provider.(type) {
	case sizeProvider:
		return scalar != "tokens"
	case tokenProvider:
		return scalar == "tokens"
	default:
		return false
	}
}

type WalkOptions struct {
	Depth    int
	StatOnly bool
}
type ContentFacts interface {
	Stat(context.Context, string) (DocScalars, error)
	Walk(context.Context, string, WalkOptions, func(DocScalars) error) error
}
type contentFacts struct {
	fs        vfs.FileSystem
	raw       vfs.FileSystem
	index     FactsIndex
	providers []ContentScalarProvider
	indexLog  *sync.Once
}

func NewContentFacts(fs vfs.FileSystem, providers ...ContentScalarProvider) ContentFacts {
	if len(providers) == 0 {
		providers = defaultProviders
	}
	return &contentFacts{fs: fs, raw: fs, providers: providers, indexLog: &sync.Once{}}
}

func newIndexedContentFacts(scoped, raw vfs.FileSystem, index FactsIndex, indexLog *sync.Once, providers []ContentScalarProvider) ContentFacts {
	return &contentFacts{fs: scoped, raw: raw, index: index, providers: providers, indexLog: indexLog}
}

func (f *contentFacts) file(ctx context.Context, p string, info *vfs.FileInfo) (DocScalars, error) {
	if f.index != nil {
		fact, hit, err := f.index.Lookup(ctx, p, info.Size(), info.ModTime().UnixNano(), sourceNames(f.providers))
		if err == nil && hit {
			return docScalarsFromIndexed(fact, f.providers)
		}
		if err != nil {
			f.indexLog.Do(func() { log.Printf("analytics facts index unavailable; computing uncached: %v", err) })
		}
	}
	b, err := f.raw.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && f.index != nil {
			_ = f.index.Delete(ctx, p)
		}
		return DocScalars{}, err
	}
	if f.index == nil {
		return computeScalars(p, b, f.providers), nil
	}
	fact := indexedFacts(p, info, b, f.providers)
	if err := f.index.Upsert(ctx, fact); err != nil {
		f.indexLog.Do(func() { log.Printf("analytics facts index unavailable; computing uncached: %v", err) })
	}
	return docScalarsFromIndexed(fact, f.providers)
}
func (f *contentFacts) Stat(ctx context.Context, p string) (DocScalars, error) {
	info, err := f.fs.Stat(vfs.CleanPath(p))
	if err != nil {
		return DocScalars{}, err
	}
	if !info.Dir {
		return f.file(ctx, p, info)
	}
	total := DocScalars{Path: vfs.CleanPath(p), Scalars: map[string]float64{}, Tokenizer: tokenizerName(f.providers), ComputedAt: time.Now().UTC()}
	err = f.Walk(ctx, p, WalkOptions{}, func(d DocScalars) error {
		if d.Path != total.Path {
			for k, v := range d.Scalars {
				total.Scalars[k] += v
			}
		}
		return nil
	})
	return total, err
}
func (f *contentFacts) Walk(ctx context.Context, prefix string, opts WalkOptions, fn func(DocScalars) error) error {
	prefix = vfs.CleanPath(prefix)
	var walk func(string, int) error
	walk = func(p string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := f.fs.Stat(p)
		if err != nil {
			return err
		}
		if !info.Dir {
			var d DocScalars
			if opts.StatOnly {
				d = DocScalars{Path: p, Scalars: map[string]float64{"bytes": float64(info.FileSize)}, ComputedAt: time.Now().UTC()}
			} else {
				var e error
				d, e = f.file(ctx, p, info)
				if e != nil {
					return e
				}
			}
			return fn(d)
		}
		if opts.Depth > 0 && depth > opts.Depth {
			return nil
		}
		entries, err := f.fs.ReadDir(p)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := walk(path.Join(p, e.FileName), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(prefix, 0)
}

func CountFiles(d DocScalars) int {
	if strings.TrimSpace(d.Path) == "" {
		return 0
	}
	return 1
}
