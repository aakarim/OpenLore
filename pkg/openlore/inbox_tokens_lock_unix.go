//go:build !windows

package openlore

import (
	"os"

	"golang.org/x/sys/unix"
)

func (s *InboxTokenStore) locked(fn func() error) error {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:errcheck
	return fn()
}
