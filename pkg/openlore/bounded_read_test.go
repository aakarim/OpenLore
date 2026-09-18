package openlore

import (
	"errors"
	"testing"
)

type countingReader struct{ read int }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.read += len(p)
	return len(p), nil
}

func TestReadAllBoundedStopsAfterLimit(t *testing.T) {
	reader := &countingReader{}
	if _, err := readAllBounded(reader, 10); !errors.Is(err, errFileTooLarge) {
		t.Fatalf("oversized read error = %v", err)
	}
	if reader.read != 11 {
		t.Fatalf("bounded read consumed %d bytes, want 11", reader.read)
	}
}
