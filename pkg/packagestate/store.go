// Package packagestate stores package-owned, per-file append-only state beside content.
package packagestate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

type HostMapper interface {
	HostDir(vfsDir string) (string, bool)
}

type Store interface {
	Dir(context.Context, string) (DirStore, error)
}

type DirStore interface {
	Append(key string, record []byte) error
	Records(key string) iter.Seq2[[]byte, error]
	Replace(key string, records [][]byte) error
	Remove(key string) error
	Move(key string, to DirStore, toKey string) error
	RewriteMove(key string, to DirStore, toKey string, rewrite func([]byte) ([]byte, error)) error
}

type store struct {
	mapper HostMapper
	pkg    string
	err    error
	mu     *sync.Mutex
}

var packageLocks sync.Map

// Open scopes a store to one package. Uppercase letters use Go module-cache
// escaping (Github.com/X becomes !github.com/!x).
func Open(mapper HostMapper, pkg string) Store {
	err := validPackage(pkg)
	escaped := escapePath(pkg)
	lock, _ := packageLocks.LoadOrStore(escaped, &sync.Mutex{})
	return &store{mapper: mapper, pkg: escaped, err: err, mu: lock.(*sync.Mutex)}
}

func validPackage(pkg string) error {
	if pkg == "" || strings.ContainsRune(pkg, 0) || strings.ContainsAny(pkg, `\:`) || strings.HasPrefix(pkg, "/") || strings.HasSuffix(pkg, "/") || path.Clean(pkg) != pkg {
		return fmt.Errorf("invalid package-state package %q", pkg)
	}
	for _, segment := range strings.Split(pkg, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid package-state package %q", pkg)
		}
	}
	return nil
}

func escapePath(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *store) Dir(_ context.Context, vfsDir string) (DirStore, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.mapper == nil {
		return unavailable{}, nil
	}
	host, ok := s.mapper.HostDir(vfsDir)
	if !ok {
		return unavailable{}, nil
	}
	return &dirStore{host: host, pkg: s.pkg, mu: s.mu}, nil
}

type unavailable struct{}

func (unavailable) Append(string, []byte) error             { return os.ErrPermission }
func (unavailable) Records(string) iter.Seq2[[]byte, error] { return func(func([]byte, error) bool) {} }
func (unavailable) Replace(string, [][]byte) error          { return os.ErrPermission }
func (unavailable) Remove(string) error                     { return nil }
func (unavailable) Move(string, DirStore, string) error     { return os.ErrPermission }
func (unavailable) RewriteMove(string, DirStore, string, func([]byte) ([]byte, error)) error {
	return os.ErrPermission
}

type dirStore struct {
	host string
	pkg  string
	mu   *sync.Mutex
}

func validKey(key string) error {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key || strings.ContainsAny(key, `/\\`) || strings.ContainsRune(key, 0) {
		return fmt.Errorf("invalid package-state key %q", key)
	}
	return nil
}

func (d *dirStore) withRoot(create bool, fn func(*os.Root) error) error {
	r, err := os.OpenRoot(d.host)
	if err != nil {
		return err
	}
	defer r.Close()
	if create {
		current := ".lore"
		if err := r.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		for _, segment := range strings.Split(d.pkg, "/") {
			current = filepath.ToSlash(filepath.Join(current, segment))
			if err := r.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
	}
	return fn(r)
}

func (d *dirStore) rel(key string) string {
	return filepath.ToSlash(filepath.Join(".lore", filepath.FromSlash(d.pkg), key))
}

func (d *dirStore) Append(key string, record []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.append(key, record)
}

func (d *dirStore) append(key string, record []byte) error {
	if err := validKey(key); err != nil {
		return err
	}
	return d.withRoot(true, func(r *os.Root) error {
		f, err := r.OpenFile(d.rel(key), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err = f.Write(append(append([]byte(nil), record...), '\n')); err != nil {
			return err
		}
		return f.Sync()
	})
}

func (d *dirStore) Records(key string) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.records(key, yield)
	}
}

func (d *dirStore) records(key string, yield func([]byte, error) bool) {
	if err := validKey(key); err != nil {
		yield(nil, err)
		return
	}
	err := d.withRoot(false, func(r *os.Root) error {
		f, err := r.Open(d.rel(key))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			if !yield(append([]byte(nil), s.Bytes()...), nil) {
				return nil
			}
		}
		return s.Err()
	})
	if err != nil {
		yield(nil, err)
	}
}

func (d *dirStore) Replace(key string, records [][]byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.replace(key, records)
}

func (d *dirStore) replace(key string, records [][]byte) error {
	if err := validKey(key); err != nil {
		return err
	}
	return d.withRoot(true, func(r *os.Root) error {
		tmp := d.rel(key) + ".tmp"
		f, err := r.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		ok := false
		defer func() {
			f.Close()
			if !ok {
				_ = r.Remove(tmp)
			}
		}()
		for _, record := range records {
			if _, err = f.Write(append(append([]byte(nil), record...), '\n')); err != nil {
				return err
			}
		}
		if err = f.Sync(); err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = r.Rename(tmp, d.rel(key)); err != nil {
			return err
		}
		ok = true
		return nil
	})
}

func (d *dirStore) Remove(key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.remove(key)
}

func (d *dirStore) remove(key string) error {
	if err := validKey(key); err != nil {
		return err
	}
	return d.withRoot(false, func(r *os.Root) error {
		err := r.Remove(d.rel(key))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

func (d *dirStore) Move(key string, target DirStore, toKey string) error {
	return d.RewriteMove(key, target, toKey, func(record []byte) ([]byte, error) { return record, nil })
}

func (d *dirStore) RewriteMove(key string, target DirStore, toKey string, rewrite func([]byte) ([]byte, error)) error {
	if err := validKey(key); err != nil {
		return err
	}
	if err := validKey(toKey); err != nil {
		return err
	}
	to, ok := target.(*dirStore)
	if !ok || to.mu != d.mu {
		return os.ErrPermission
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var records [][]byte
	var readErr error
	d.records(key, func(record []byte, err error) bool {
		if err != nil {
			readErr = err
			return false
		}
		rewritten, err := rewrite(record)
		if err != nil {
			readErr = err
			return false
		}
		records = append(records, rewritten)
		return true
	})
	if readErr != nil {
		return readErr
	}
	if len(records) == 0 {
		return nil
	}
	if err := to.replace(toKey, records); err != nil {
		return err
	}
	return d.remove(key)
}
