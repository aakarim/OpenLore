package openlore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/fxamacker/cbor/v2"
)

const (
	xattrFormat      = "openlore.xattrs"
	xattrVersion     = uint64(1)
	xattrValueMax    = 65536
	xattrListMax     = 65536
	xattrEnvelopeMax = 8 << 20
)

type xattrBody struct {
	Format     string            `cbor:"format"`
	Version    uint64            `cbor:"version"`
	Attributes map[string][]byte `cbor:"attributes"`
}

type xattrEnvelope struct {
	Format     string            `cbor:"format"`
	Version    uint64            `cbor:"version"`
	Attributes map[string][]byte `cbor:"attributes"`
	Checksum   []byte            `cbor:"checksum"`
}

var xattrEnc cbor.EncMode

func init() { xattrEnc, _ = cbor.CanonicalEncOptions().EncMode() }

func validateXattrName(name string) error {
	if len(name) > 255 {
		return syscall.ERANGE
	}
	if !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 {
		return syscall.EINVAL
	}
	if !strings.HasPrefix(name, "user.lore.") || len(name) == len("user.lore.") {
		return syscall.ENOTSUP
	}
	return nil
}

func encodeXattrs(attrs map[string][]byte) ([]byte, error) {
	body := xattrBody{xattrFormat, xattrVersion, attrs}
	b, err := xattrEnc.Marshal(body)
	if err != nil {
		return nil, syscall.EIO
	}
	sum := sha256.Sum256(b)
	out, err := xattrEnc.Marshal(xattrEnvelope{xattrFormat, xattrVersion, attrs, sum[:]})
	if err != nil {
		return nil, syscall.EIO
	}
	if len(out) > xattrEnvelopeMax {
		return nil, syscall.ENOSPC
	}
	return out, nil
}

func decodeXattrs(b []byte) (map[string][]byte, error) {
	if b == nil {
		return map[string][]byte{}, nil
	}
	var env xattrEnvelope
	dec, _ := cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, MaxNestedLevels: 16}.DecMode()
	if len(b) > xattrEnvelopeMax {
		return nil, syscall.EIO
	}
	if err := dec.Unmarshal(b, &env); err != nil || env.Format != xattrFormat || env.Version != xattrVersion || env.Attributes == nil {
		return nil, syscall.EIO
	}
	body, err := xattrEnc.Marshal(xattrBody{env.Format, env.Version, env.Attributes})
	if err != nil {
		return nil, syscall.EIO
	}
	sum := sha256.Sum256(body)
	if !bytes.Equal(env.Checksum, sum[:]) {
		return nil, syscall.EIO
	}
	canonical, err := xattrEnc.Marshal(env)
	if err != nil || !bytes.Equal(canonical, b) {
		return nil, syscall.EIO
	}
	listSize := 0
	for n, v := range env.Attributes {
		if validateXattrName(n) != nil || len(v) > xattrValueMax {
			return nil, syscall.EIO
		}
		listSize += len(n) + 1
	}
	if listSize > xattrListMax {
		return nil, syscall.EIO
	}
	return env.Attributes, nil
}

func loadXattrs(folder xattrDir) (map[string][]byte, error) {
	b, err := folder.readSelf()
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, normalizeXattrIO(err)
	}
	return decodeXattrs(b)
}

func normalizeXattrIO(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return syscall.ENOENT
	}
	if errors.Is(err, fs.ErrPermission) {
		return syscall.EPERM
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return syscall.ENOSPC
	}
	return syscall.EIO
}

func (d *DirFS) xattrTarget(p string) (xattrDir, error) {
	if hasReservedPath(p) || hasTraversal(p) {
		return xattrDir{}, syscall.EPERM
	}
	return openXattrDir(d.root, vfs.CleanPath(p))
}

func (d *DirFS) GetXattr(p, name string) ([]byte, error) {
	if err := validateXattrName(name); err != nil {
		return nil, err
	}
	full, err := d.xattrTarget(p)
	if err != nil {
		return nil, err
	}
	defer full.close()
	attrs, err := loadXattrs(full)
	if err != nil {
		return nil, err
	}
	v, ok := attrs[name]
	if !ok {
		return nil, syscall.ENODATA
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (d *DirFS) ListXattrs(p string) ([]string, error) {
	full, err := d.xattrTarget(p)
	if err != nil {
		return nil, err
	}
	defer full.close()
	attrs, err := loadXattrs(full)
	if err != nil {
		return nil, err
	}
	names, size := make([]string, 0, len(attrs)), 0
	for n := range attrs {
		names = append(names, n)
		size += len(n) + 1
	}
	if size > xattrListMax {
		return nil, syscall.ERANGE
	}
	sort.Strings(names)
	return names, nil
}

func (d *DirFS) SetXattr(p, name string, value []byte, flags vfs.XattrFlags) error {
	if !flags.Valid() {
		return syscall.EINVAL
	}
	if err := validateXattrName(name); err != nil {
		return err
	}
	if len(value) > xattrValueMax {
		return syscall.ERANGE
	}
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return syscall.EPERM
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()
	full, err := d.xattrTarget(p)
	if err != nil {
		return err
	}
	defer full.close()
	attrs, err := loadXattrs(full)
	if err != nil {
		return err
	}
	_, exists := attrs[name]
	if flags == vfs.XattrCreate && exists {
		return syscall.EEXIST
	}
	if flags == vfs.XattrReplace && !exists {
		return syscall.ENODATA
	}
	attrs[name] = make([]byte, len(value))
	copy(attrs[name], value)
	listSize := 0
	for n := range attrs {
		listSize += len(n) + 1
	}
	if listSize > xattrListMax {
		return syscall.ERANGE
	}
	return persistXattrs(full, attrs)
}

func (d *DirFS) RemoveXattr(p, name string) error {
	if err := validateXattrName(name); err != nil {
		return err
	}
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return syscall.EPERM
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()
	full, err := d.xattrTarget(p)
	if err != nil {
		return err
	}
	defer full.close()
	attrs, err := loadXattrs(full)
	if err != nil {
		return err
	}
	if _, ok := attrs[name]; !ok {
		return syscall.ENODATA
	}
	delete(attrs, name)
	return persistXattrs(full, attrs)
}

func persistXattrs(folder xattrDir, attrs map[string][]byte) error {
	b, err := encodeXattrs(attrs)
	if err != nil {
		return err
	}
	if err := folder.writeSelf(b); err != nil {
		return normalizeXattrIO(err)
	}
	return nil
}

func validateFreshAttrs(attrs map[string][]byte) error {
	list := 0
	for n, v := range attrs {
		if err := validateXattrName(n); err != nil {
			return err
		}
		if len(v) > xattrValueMax {
			return syscall.ERANGE
		}
		list += len(n) + 1
	}
	if list > xattrListMax {
		return syscall.ERANGE
	}
	_, err := encodeXattrs(attrs)
	return err
}

func (d *DirFS) PreserveAndRecreateXattrs(p string, attrs map[string][]byte) error {
	if err := validateFreshAttrs(attrs); err != nil {
		return err
	}
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return syscall.EPERM
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()
	f, err := d.xattrTarget(p)
	if err != nil {
		return err
	}
	defer f.close()
	raw, err := f.readSelf()
	if err != nil {
		return normalizeXattrIO(err)
	}
	if _, err = decodeXattrs(raw); err == nil {
		return &vfs.XattrStaleError{}
	}
	sum := sha256.Sum256(raw)
	backup := "self.conflicted." + hex.EncodeToString(sum[:])
	if err = f.createRelative(backup, raw); err != nil {
		if !errors.Is(err, syscall.EEXIST) {
			return normalizeXattrIO(err)
		}
		existing, e := f.readRelative(backup)
		if e != nil || !bytes.Equal(existing, raw) {
			return syscall.EIO
		}
		es := sha256.Sum256(existing)
		if es != sum {
			return syscall.EIO
		}
	}
	return persistXattrs(f, attrs)
}

func (d *DirFS) MigrateXattrs(p string, m vfs.XattrMigration) error {
	if !strings.HasPrefix(m.NamespacePrefix, "user.lore.plugins.") {
		return syscall.ENOTSUP
	}
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	if !d.writeable {
		return syscall.EPERM
	}
	d.commitMu.Lock()
	defer d.commitMu.Unlock()
	f, err := d.xattrTarget(p)
	if err != nil {
		return err
	}
	defer f.close()
	raw, err := f.readSelf()
	if err != nil {
		return normalizeXattrIO(err)
	}
	sum := sha256.Sum256(raw)
	if !bytes.Equal(sum[:], m.ExpectedEnvelopeSHA256) {
		return &vfs.XattrStaleError{}
	}
	attrs, err := decodeXattrs(raw)
	if err != nil {
		return err
	}
	for _, e := range m.Edits {
		if !strings.HasPrefix(e.Name, m.NamespacePrefix) || e.Name == m.NamespacePrefix {
			return syscall.EPERM
		}
		if err = validateXattrName(e.Name); err != nil {
			return err
		}
		if e.Remove {
			delete(attrs, e.Name)
		} else {
			attrs[e.Name] = append([]byte(nil), e.Value...)
		}
	}
	if err = validateFreshAttrs(attrs); err != nil {
		return err
	}
	return persistXattrs(f, attrs)
}
