package cmds_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"syscall"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type skillsFS struct {
	*mapFS
	attrs    map[string]map[string][]byte
	conflict map[string]bool
	repairs  []map[string][]byte
}

func newSkillsFS() *skillsFS {
	f := &skillsFS{mapFS: testFS(), attrs: map[string]map[string][]byte{}, conflict: map[string]bool{}}
	f.AddDir("/docs/skills")
	return f
}
func (f *skillsFS) GetXattr(p, n string) ([]byte, error) {
	if f.conflict[p] {
		return nil, syscall.EIO
	}
	b, ok := f.attrs[p][n]
	if !ok {
		return nil, syscall.ENODATA
	}
	return append([]byte(nil), b...), nil
}
func (f *skillsFS) ListXattrs(p string) ([]string, error) {
	if f.conflict[p] {
		return nil, syscall.EIO
	}
	var out []string
	for n := range f.attrs[p] {
		out = append(out, n)
	}
	return out, nil
}
func (f *skillsFS) SetXattr(p, n string, b []byte, _ vfs.XattrFlags) error {
	if f.attrs[p] == nil {
		f.attrs[p] = map[string][]byte{}
	}
	f.attrs[p][n] = append([]byte(nil), b...)
	return nil
}
func (f *skillsFS) RemoveXattr(p, n string) error {
	if _, ok := f.attrs[p][n]; !ok {
		return syscall.ENODATA
	}
	delete(f.attrs[p], n)
	return nil
}
func (f *skillsFS) PreserveAndRecreateXattrs(p string, a map[string][]byte) error {
	f.repairs = append(f.repairs, a)
	f.conflict[p] = false
	f.attrs[p] = a
	return nil
}
func (*skillsFS) MigrateXattrs(string, vfs.XattrMigration) error { return errors.New("not used") }

func runSkills(t *testing.T, f *skillsFS, cwd, command string, enabled bool, grant string) (map[string]any, string, int) {
	t.Helper()
	sh := shell.NewShell(f)
	sh.SetCwd(cwd)
	sh.SetSkillsManagementEnabled(enabled)
	sh.SetDocsets([]cmds.DocsetInfo{{Name: "docs", Paths: []string{"/docs"}, Writable: grant == "rw", Grants: []string{grant}}})
	var out bytes.Buffer
	code := sh.ExecPipeline(command, &out, &bytes.Buffer{}, nil)
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	var result map[string]any
	if len(lines) > 0 {
		if err := json.Unmarshal(lines[len(lines)-1], &result); err != nil {
			t.Fatalf("invalid NDJSON %q: %v", out.String(), err)
		}
	}
	return result, out.String(), code
}

func TestSkillsDisabledResultHasRequiredFields(t *testing.T) {
	r, _, code := runSkills(t, newSkillsFS(), "/docs/skills", "skills status", false, "rw")
	if code == 0 || r["status"] != "unsupported" || r["path"] != "/docs/skills" || r["operation"] != "status" {
		t.Fatalf("result=%v code=%d", r, code)
	}
	for _, k := range []string{"collections", "errors", "warnings"} {
		if _, ok := r[k]; !ok {
			t.Fatalf("missing %s: %v", k, r)
		}
	}
}

func TestSkillsEnableRecursiveValidationAndIdempotence(t *testing.T) {
	f := newSkillsFS()
	f.AddDir("/docs/skills/deploy")
	f.AddFile("/docs/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: deploys\n---\n")
	r, _, code := runSkills(t, f, "/docs/skills", "skills enable", true, "rw")
	if code != 0 || r["status"] != "enabled" {
		t.Fatalf("%v %d", r, code)
	}
	r, _, code = runSkills(t, f, "/docs/skills", "skills enable", true, "rw")
	if code != 0 || r["status"] != "already_enabled" {
		t.Fatalf("%v %d", r, code)
	}
}

func TestSkillsInvalidAndNamedRWDenial(t *testing.T) {
	f := newSkillsFS()
	f.AddDir("/docs/skills/bad")
	f.AddFile("/docs/skills/bad/SKILL.md", "no frontmatter")
	r, out, code := runSkills(t, f, "/docs/skills", "skills enable", true, "rw")
	if code == 0 || r["status"] != "rejected" || !bytes.Contains([]byte(out), []byte(`"line":1`)) {
		t.Fatalf("%v %d %s", r, code, out)
	}
	r, _, code = runSkills(t, newSkillsFS(), "/docs/skills", "skills enable", true, "publish")
	if code == 0 || r["status"] != "rejected" {
		t.Fatalf("%v %d", r, code)
	}
}

func TestSkillsValidateDefaultAndDisabledFrontmatter(t *testing.T) {
	f := newSkillsFS()
	f.attrs["/docs/skills"] = map[string][]byte{cmds.SkillsMarker: {}}
	f.AddDir("/docs/skills/off")
	f.AddFile("/docs/skills/off/SKILL.md", "---\nmetadata:\n  agent_skill: disable\nunknown: accepted\n---\n")
	r, _, code := runSkills(t, f, "/docs/skills", "skills validate", true, "ro")
	if code != 0 || r["path"] != "/docs/skills" || r["status"] != "valid" {
		t.Fatalf("%v %d", r, code)
	}
	r, _, code = runSkills(t, newSkillsFS(), "/docs/skills", "skills validate", true, "ro")
	if code != 0 || r["status"] != "no_collections" || r["warnings"] != float64(1) {
		t.Fatalf("%v %d", r, code)
	}
}

func TestSkillsDisableInheritedAndExplicitRecreate(t *testing.T) {
	f := newSkillsFS()
	f.attrs["/docs"] = map[string][]byte{cmds.SkillsMarker: {}}
	f.attrs["/docs/skills"] = map[string][]byte{cmds.SkillsMarker: {}}
	r, _, code := runSkills(t, f, "/docs/skills", "skills disable", true, "rw")
	if code != 0 || r["status"] != "marker_removed" || r["source"] != "/docs" {
		t.Fatalf("%v %d", r, code)
	}
	f.conflict["/docs/skills"] = true
	r, _, code = runSkills(t, f, "/docs/skills", "skills enable --recreate-xattrs", true, "rw")
	if code != 0 || r["status"] != "enabled" || len(f.repairs) != 1 {
		t.Fatalf("%v %d repairs=%d", r, code, len(f.repairs))
	}
	r, _, code = runSkills(t, f, "/docs/skills", "skills enable --recreate-xattrs", true, "rw")
	if code == 0 || r["status"] != "rejected" || len(f.repairs) != 1 {
		t.Fatalf("%v %d repairs=%d", r, code, len(f.repairs))
	}
}
