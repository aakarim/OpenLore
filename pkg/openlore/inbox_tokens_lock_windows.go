//go:build windows

package openlore

import (
	"os"

	"golang.org/x/sys/windows"
)

func (s *InboxTokenStore) locked(fn func() error) error {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		return err
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped) //nolint:errcheck
	return fn()
}
