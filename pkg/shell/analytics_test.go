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

func TestCommandObserverSeesXargsGeneratedCommands(t *testing.T) {
	sh := NewShell(analyticsTestFS{})
	var got []CommandExecution
	sh.SetCommandObserver(func(e CommandExecution) { got = append(got, e) })
	var out bytes.Buffer
	if code := sh.ExecPipeline("printf 'one two' | xargs -n 1 echo", &out, &out, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(got) != 4 {
		t.Fatalf("observations = %#v, want printf, two echo calls, and xargs", got)
	}
	var echoes int
	for _, observation := range got {
		if observation.Command == "echo" {
			echoes++
			if observation.InvocationID == "" || observation.BytesOut == 0 {
				t.Fatalf("generated command was not fully observed: %#v", observation)
			}
		}
	}
	if echoes != 2 {
		t.Fatalf("observations = %#v, want two generated echo commands", got)
	}
}
