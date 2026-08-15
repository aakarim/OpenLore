//go:build !darwin && !linux

package openlore

import "syscall"

type xattrDir struct{}

func (xattrDir) close()                              {}
func openXattrDir(string, string) (xattrDir, error)  { return xattrDir{}, syscall.ENOTSUP }
func (xattrDir) readSelf() ([]byte, error)           { return nil, syscall.ENOTSUP }
func (xattrDir) writeSelf([]byte) error              { return syscall.ENOTSUP }
func (xattrDir) readRelative(string) ([]byte, error) { return nil, syscall.ENOTSUP }
func (xattrDir) createRelative(string, []byte) error { return syscall.ENOTSUP }
