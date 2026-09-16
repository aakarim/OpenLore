package vfs

import (
	"errors"
	"io/fs"
	"testing"
)

func TestErrNotFoundMatchesStandardSentinel(t *testing.T) {
	err := ErrNotFound("/missing")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("errors.Is(%v, fs.ErrNotExist) = false, want true", err)
	}
	if err.Error() != "not found: /missing" {
		t.Fatalf("error = %q, want existing message preserved", err)
	}
}
