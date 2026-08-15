//go:build darwin || linux

package openlore

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type xattrDir struct{ fd int }

func (d xattrDir) close() { _ = unix.Close(d.fd) }

func openXattrDir(root, name string) (xattrDir, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return xattrDir{}, normalizeOpenat(err)
	}
	for _, part := range strings.Split(strings.Trim(name, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if e != nil {
			return xattrDir{}, normalizeOpenat(e)
		}
		fd = next
	}
	return xattrDir{fd}, nil
}

func normalizeOpenat(err error) error {
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return syscall.ENOTSUP
	}
	return normalizeXattrIO(err)
}

func (d xattrDir) metadata(create bool) (int, error) {
	fd := d.fd
	for _, n := range []string{".lore", "xattrs"} {
		next, err := unix.Openat(fd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil && create && errors.Is(err, syscall.ENOENT) {
			if err = unix.Mkdirat(fd, n, 0700); err != nil && !errors.Is(err, syscall.EEXIST) {
				return -1, normalizeXattrIO(err)
			}
			if err = unix.Fsync(fd); err != nil {
				return -1, normalizeXattrIO(err)
			}
			next, err = unix.Openat(fd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if fd != d.fd {
			_ = unix.Close(fd)
		}
		if err != nil {
			return -1, normalizeOpenat(err)
		}
		fd = next
	}
	return fd, nil
}

func (d xattrDir) readSelf() ([]byte, error) {
	return d.readRelative("self")
}

func (d xattrDir) readRelative(name string) ([]byte, error) {
	md, err := d.metadata(false)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, fs.ErrNotExist
		}
		return nil, err
	}
	defer unix.Close(md)
	fd, err := unix.Openat(md, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, fs.ErrNotExist
		}
		return nil, normalizeOpenat(err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return nil, normalizeXattrIO(err)
	}
	if st.Size < 0 || uint64(st.Size) > uint64(^uint(0)>>1) {
		return nil, syscall.EIO
	}
	b := make([]byte, st.Size)
	for off := 0; off < len(b); {
		n, e := unix.Read(fd, b[off:])
		if e != nil {
			return nil, normalizeXattrIO(e)
		}
		if n == 0 {
			return nil, syscall.EIO
		}
		off += n
	}
	return b, nil
}

func (d xattrDir) createRelative(name string, b []byte) error {
	md, err := d.metadata(true)
	if err != nil {
		return err
	}
	defer unix.Close(md)
	fd, err := unix.Openat(md, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	for len(b) > 0 {
		n, e := unix.Write(fd, b)
		if e != nil {
			return normalizeXattrIO(e)
		}
		b = b[n:]
	}
	if err = unix.Fsync(fd); err != nil {
		return normalizeXattrIO(err)
	}
	return normalizeXattrIO(unix.Fsync(md))
}

func (d xattrDir) writeSelf(b []byte) error {
	md, err := d.metadata(true)
	if err != nil {
		return err
	}
	defer unix.Close(md)
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return syscall.EIO
	}
	tmp := fmt.Sprintf(".self.tmp.%x", nonce[:])
	fd, err := unix.Openat(md, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return normalizeXattrIO(err)
	}
	ok := false
	defer func() {
		_ = unix.Close(fd)
		if !ok {
			_ = unix.Unlinkat(md, tmp, 0)
		}
	}()
	for len(b) > 0 {
		n, e := unix.Write(fd, b)
		if e != nil {
			return normalizeXattrIO(e)
		}
		b = b[n:]
	}
	if err = unix.Fsync(fd); err != nil {
		return normalizeXattrIO(err)
	}
	if err = unix.Renameat(md, tmp, md, "self"); err != nil {
		return normalizeXattrIO(err)
	}
	ok = true
	if err = unix.Fsync(md); err != nil {
		return normalizeXattrIO(err)
	}
	return nil
}
