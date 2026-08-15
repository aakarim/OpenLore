package cmds_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/aakarim/go-openlore/pkg/agentskills"
	"github.com/aakarim/go-openlore/pkg/openlore/skillsremote"
	"github.com/aakarim/go-openlore/pkg/shell"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type skillsFS struct {
	*mapFS
	attrs     map[string]map[string][]byte
	conflict  map[string]bool
	repairs   []map[string][]byte
	updateOld string
	updateNew string
	updateErr error
	remote    *skillsremote.Client
	admitted  []vfs.ChangeSet
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
func (f *skillsFS) UpdateRemoteSkill(context.Context, string) (string, string, error) {
	return f.updateOld, f.updateNew, f.updateErr
}
func (f *skillsFS) SkillsRemoteClient() *skillsremote.Client { return f.remote }
func (f *skillsFS) AdmitChangeSet(cs vfs.ChangeSet) error {
	f.admitted = append(f.admitted, cs)
	return nil
}

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

func TestSkillsBlankAndHelpShowImportInstructions(t *testing.T) {
	for _, command := range []string{"skills", "skills help", "skills --help"} {
		sh := shell.NewShell(newSkillsFS())
		var out bytes.Buffer
		if code := sh.ExecPipeline(command, &out, &bytes.Buffer{}, nil); code != 0 {
			t.Fatalf("%s exit = %d", command, code)
		}
		for _, want := range []string{"# Managing Agent Skills", "lore meta --filter skills", "skills enable \"$HOME\"", "skills import https://github.com/owner/repo \"$HOME\"", "candidate@main", "tracks updates"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%s missing %q:\n%s", command, want, out.String())
			}
		}
	}
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

func TestSkillsUpdateReportsCommitTransition(t *testing.T) {
	f := newSkillsFS()
	f.updateOld = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f.updateNew = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	r, _, code := runSkills(t, f, "/docs/skills", "skills update", true, "rw")
	if code != 0 || r["status"] != "updated" || r["old_commit"] != f.updateOld || r["new_commit"] != f.updateNew {
		t.Fatalf("result=%v code=%d", r, code)
	}
	f.updateErr = errors.New("offline")
	r, _, code = runSkills(t, f, "/docs/skills", "skills update", true, "rw")
	if code == 0 || r["status"] != "degraded" {
		t.Fatalf("result=%v code=%d", r, code)
	}
}

func remoteSkillsTestClient(t *testing.T, skill string) *skillsremote.Client {
	return remoteSkillsTestClientFiles(t, map[string]string{
		"skills/grill/SKILL.md":              skill,
		"skills/grill/references/example.md": "example",
	})
}

func remoteSkillsTestClientFiles(t *testing.T, files map[string]string) *skillsremote.Client {
	t.Helper()
	sha := strings.Repeat("a", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/info/refs", func(w http.ResponseWriter, _ *http.Request) {
		packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
		fmt.Fprint(w, packet("# service=git-upload-pack\n")+"0000"+packet(sha+" HEAD\x00symref=HEAD:refs/heads/main\n")+packet(sha+" refs/heads/main\n")+"0000")
	})
	mux.HandleFunc("/owner/repo/tar.gz/"+sha, func(w http.ResponseWriter, _ *http.Request) {
		gz := gzip.NewWriter(w)
		archive := tar.NewWriter(gz)
		for name, content := range files {
			_ = archive.WriteHeader(&tar.Header{Name: "repo-x/" + name, Size: int64(len(content)), Typeflag: tar.TypeReg})
			_, _ = archive.Write([]byte(content))
		}
		_ = archive.Close()
		_ = gz.Close()
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &skillsremote.Client{HTTP: server.Client(), GitHubBase: server.URL, CodeloadBase: server.URL}
}

func TestSkillsImportRejectsUnsafeNameBeforeMutation(t *testing.T) {
	f := newSkillsFS()
	f.attrs["/docs/skills"] = map[string][]byte{cmds.SkillsMarker: {}}
	f.remote = remoteSkillsTestClient(t, "---\nname: ../...\ndescription: unsafe\n---\n")
	r, _, code := runSkills(t, f, "/docs/skills", "skills import owner/repo/skills/grill@main", true, "rw")
	if code == 0 || r["status"] != "rejected" || len(f.admitted) != 0 {
		t.Fatalf("result=%v code=%d admitted=%d", r, code, len(f.admitted))
	}
}

func TestSkillsImportRejectsBeforeNetworkWhenPluginDisabled(t *testing.T) {
	f := newSkillsFS()
	f.attrs["/docs/skills"] = map[string][]byte{cmds.SkillsMarker: {}}
	f.remote = remoteSkillsTestClient(t, "---\nname: grill\ndescription: useful\n---\n")
	r, _, code := runSkills(t, f, "/docs/skills", "skills import owner/repo/skills/grill@main", false, "rw")
	if code == 0 || r["status"] != "unsupported" || len(f.admitted) != 0 {
		t.Fatalf("result=%v code=%d admitted=%d", r, code, len(f.admitted))
	}
}

func TestSkillsImportAmbiguousRemoteEmitsSortedCandidatesWithoutMutation(t *testing.T) {
	f := newSkillsFS()
	f.attrs["/docs/skills"] = map[string][]byte{cmds.SkillsMarker: {}}
	f.remote = remoteSkillsTestClientFiles(t, map[string]string{
		"zeta/SKILL.md":  "---\nname: zeta\ndescription: zeta\n---\n",
		"alpha/SKILL.md": "---\nname: alpha\ndescription: alpha\n---\n",
	})
	filesBefore := len(f.Files)
	r, out, code := runSkills(t, f, "/docs/skills", "skills import owner/repo@main", true, "rw")
	if code == 0 || r["status"] != "rejected" || r["errors"] != float64(2) {
		t.Fatalf("result=%v code=%d output=%s", r, code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("records=%q", lines)
	}
	for i, wantPath := range []string{"alpha", "zeta"} {
		var candidate map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &candidate); err != nil {
			t.Fatalf("candidate %d: %v", i, err)
		}
		if candidate["type"] != "candidate" || candidate["path"] != wantPath || candidate["name"] != wantPath || candidate["description"] != wantPath {
			t.Fatalf("candidate %d=%v, want path %q", i, candidate, wantPath)
		}
	}
	if len(f.admitted) != 0 || len(f.Files) != filesBefore {
		t.Fatalf("mutation occurred: admitted=%d files before=%d after=%d", len(f.admitted), filesBefore, len(f.Files))
	}
}

func TestSkillsImportRootWinsAndNormalizesSkillsSHFrontmatter(t *testing.T) {
	f := newSkillsFS()
	f.attrs["/docs/skills"] = map[string][]byte{cmds.SkillsMarker: {}}
	f.remote = remoteSkillsTestClientFiles(t, map[string]string{
		"SKILL.md":         "---\nname: My Fancy_Skill\ndescription: useful\nargument-hint: 7\n---\n",
		"example/SKILL.md": "---\nname: example\ndescription: nested\n---\n",
	})
	r, out, code := runSkills(t, f, "/docs/skills", "skills import owner/repo@main", true, "rw")
	if code != 0 || r["status"] != "imported" || r["path"] != "/docs/skills/my-fancy-skill" || len(f.admitted) != 1 {
		t.Fatalf("result=%v code=%d output=%s admitted=%d", r, code, out, len(f.admitted))
	}
	leaves := f.admitted[0].Leaves()
	stored := leaves[len(leaves)-1].Write.Bytes
	for _, want := range []string{"name: my-fancy-skill", "display-name: My Fancy_Skill", "argument-hint: \"7\"", "repo: https://github.com/owner/repo"} {
		if !bytes.Contains(stored, []byte(want)) {
			t.Fatalf("missing %q:\n%s", want, stored)
		}
	}
}

func TestSkillsImportSubmitsOneOrderedBatch(t *testing.T) {
	f := newSkillsFS()
	f.attrs["/docs/skills"] = map[string][]byte{cmds.SkillsMarker: {}}
	f.remote = remoteSkillsTestClient(t, "---\nname: grill\ndescription: useful\n---\n")
	refs, err := f.remote.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	sha, _, _, err := refs.Resolve("main")
	if err != nil {
		t.Fatal(err)
	}
	files, err := f.remote.Fetch(context.Background(), "owner/repo", sha, "skills/grill")
	if err != nil {
		t.Fatalf("test remote invalid: %v", err)
	}
	grafted, err := agentskills.GraftRemote(files["SKILL.md"], agentskills.Remote{Repo: "owner/repo", Path: "skills/grill", Ref: "main", Commit: sha, Kind: "tracking"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentskills.Validate("grill", grafted); err != nil {
		t.Fatalf("test graft invalid: %v\n%s", err, grafted)
	}
	r, _, code := runSkills(t, f, "/docs/skills", "skills import owner/repo/skills/grill@main", true, "rw")
	if code != 0 || r["status"] != "imported" || len(f.admitted) != 1 {
		t.Fatalf("result=%v code=%d admitted=%d", r, code, len(f.admitted))
	}
	leaves := f.admitted[0].Leaves()
	if len(leaves) != 4 || leaves[0].Action != vfs.ChangeActionMkdir || leaves[len(leaves)-1].Target != "/docs/skills/grill/SKILL.md" {
		t.Fatalf("batch=%+v", leaves)
	}
}
