package analytics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
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
	d := DocScalars{Path: vfs.CleanPath(p), ContentHash: hex.EncodeToString(h[:]), Scalars: map[string]float64{}, Tokenizer: "approx", ComputedAt: time.Now().UTC()}
	for _, provider := range providers {
		for k, v := range provider.Scalars(p, b) {
			d.Scalars[k] = v
		}
		if provider, ok := provider.(interface{ tokenizerName() string }); ok {
			d.Tokenizer = provider.tokenizerName()
		}
	}
	return d
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
	providers []ContentScalarProvider
}

func NewContentFacts(fs vfs.FileSystem, providers ...ContentScalarProvider) ContentFacts {
	if len(providers) == 0 {
		providers = defaultProviders
	}
	return &contentFacts{fs, providers}
}
func (f *contentFacts) Stat(ctx context.Context, p string) (DocScalars, error) {
	info, err := f.fs.Stat(vfs.CleanPath(p))
	if err != nil {
		return DocScalars{}, err
	}
	if !info.Dir {
		b, err := f.fs.ReadFile(p)
		if err != nil {
			return DocScalars{}, err
		}
		return computeScalars(p, b, f.providers), nil
	}
	total := DocScalars{Path: vfs.CleanPath(p), Scalars: map[string]float64{}, Tokenizer: "approx", ComputedAt: time.Now().UTC()}
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
				b, e := f.fs.ReadFile(p)
				if e != nil {
					return e
				}
				d = computeScalars(p, b, f.providers)
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
