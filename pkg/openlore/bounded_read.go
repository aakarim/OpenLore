package openlore

import (
	"errors"
	"fmt"
	"io"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

var (
	errBoundedReadUnsupported = errors.New("filesystem does not support bounded reads")
	errFileTooLarge           = errors.New("file exceeds read limit")
)

type boundedFileReader interface {
	ReadFileBounded(string, int64) ([]byte, error)
}

func readFileBounded(fsys vfs.FileSystem, name string, maxBytes int64) ([]byte, error) {
	reader, ok := fsys.(boundedFileReader)
	if !ok {
		return nil, errBoundedReadUnsupported
	}
	return reader.ReadFileBounded(name, maxBytes)
}

func readAllBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("invalid read limit %d", maxBytes)
	}
	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, errFileTooLarge
	}
	return content, nil
}
