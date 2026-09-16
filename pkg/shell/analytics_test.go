package shell

import (
	"bytes"
	"io/fs"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type analyticsTestFS struct{}

func (analyticsTestFS) Stat(string) (*vfs.FileInfo, error)     { return nil, fs.ErrNotExist }
func (analyticsTestFS) ReadDir(string) ([]vfs.FileInfo, error) { return nil, fs.ErrNotExist }
func (analyticsTestFS) ReadFile(string) ([]byte, error)        { return nil, fs.ErrNotExist }

func TestCommandObserverSeesPipelineCommandsAndOutput(t *testing.T) {
	sh := NewShell(analyticsTestFS{})
	var got []CommandExecution
	sh.SetCommandObserver(func(e CommandExecution) { got = append(got, e) })
	var out bytes.Buffer
	if code := sh.ExecPipeline("echo hello | wc -c", &out, &out, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(got) != 2 || got[0].Command != "echo" || got[0].PipelinePosition != 0 || got[0].BytesOut != 6 || got[1].Command != "wc" || got[1].PipelinePosition != 1 || got[0].InvocationID == "" || got[0].InvocationID != got[1].InvocationID {
		t.Fatalf("unexpected observations: %#v", got)
	}
}
