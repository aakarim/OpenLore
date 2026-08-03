package openlore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func skillBytes(name string) []byte {
	return []byte("---\nname: " + name + "\ndescription: useful\n---\n")
}

func TestAgentSkillsAdmissionRootFilesAndNestedRoots(t *testing.T) {
	docsets := map[string]config.DocsetSpec{
		"outer": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}, AgentSkills: true},
		"inner": {Paths: []config.PathMapping{{Source: "/skills/team", Display: "/skills/team"}}, AgentSkills: true},
	}
	p := newAgentSkills(docsets, failingReadFS{err: os.ErrNotExist}, nil, slog.Default())
	rootFile := vfs.ChangeSet{Target: "/skills/README.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("docs")}}
	if err := p.validateMutation(rootFile); err != nil {
		t.Fatalf("outer root-level file must be ignored: %v", err)
	}
	// It is root-level for the inner collection, but supporting content in the
	// outer collection's immediate child, so the outer SKILL.md is required.
	innerRootFile := rootFile
	innerRootFile.Target = "/skills/team/README.md"
	if err := p.validateMutation(innerRootFile); err == nil || !strings.Contains(err.Error(), "/skills/team") {
		t.Fatalf("nested root must retain enabled ancestor evaluation: %v", err)
	}
}

func TestAgentSkillsRejectsForbiddenLaterBatchLeaf(t *testing.T) {
	p := newAgentSkills(map[string]config.DocsetSpec{
		"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}, AgentSkills: true},
	}, failingReadFS{err: os.ErrNotExist}, nil, slog.Default())
	cs := vfs.ChangeSet{Changes: []vfs.Change{
		{Target: "/README.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("safe")}},
		{Target: "/skills/bad/SKILL.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: skillBytes("wrong")}},
	}}
	reached := false
	h := p.WriteMiddleware()[0](func(context.Context, WriteOp) (WriteResult, error) { reached = true; return WriteResult{}, nil })
	if _, err := h(context.Background(), NewWriteOp(Actor{}, cs)); err == nil || reached {
		t.Fatalf("later invalid leaf was not rejected: reached=%v err=%v", reached, err)
	}
}

func TestAgentSkillsAnnotationsAndAuthoritativePreApply(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "valid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "valid", "SKILL.md"), skillBytes("valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := NewDirFS(root, config.FilesConfig{})
	if err := dir.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	docsets := map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}, AgentSkills: true}}
	p := newAgentSkills(docsets, dir, nil, slog.Default())
	ext := p.MetaExtenders()[0]
	if got := ext("/skills/valid/SKILL.md", skillBytes("valid"), nil); got["agent_skill"] != true {
		t.Fatalf("valid annotation = %v", got)
	}
	if got := ext("/skills/valid/SKILL.md", skillBytes("wrong"), nil); got["agent_skill"] != false {
		t.Fatalf("invalid annotation = %v", got)
	}
	disabled := []byte("---\nmetadata:\n  agent_skill: disable\n---\n")
	if got := ext("/skills/valid/SKILL.md", disabled, nil); got != nil {
		t.Fatalf("disabled annotation = %v", got)
	}
	forgedDisabled := meta.Record{Fields: map[string]any{
		"agent_skill": true,
		"metadata":    map[string]any{"agent_skill": "disable"},
	}}
	if p.MetaFilters()[0].Selector("/skills/valid/SKILL.md", forgedDisabled) {
		t.Fatal("disabled skill forged its way into filtered discovery")
	}

	l := newWriteLog(dir, nil, slog.Default(), 1)
	l.SetPreApply(p.validateMutation)
	defer l.Close(context.Background())
	bad := vfs.ChangeSet{Target: "/skills/bad/SKILL.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: skillBytes("wrong")}}
	if _, err := l.Submit(context.Background(), Actor{}, bad); err == nil {
		t.Fatal("direct/deferred commit bypass must reject invalid skill")
	}
	removeSkill := vfs.ChangeSet{Target: "/skills/valid/SKILL.md", Action: vfs.ChangeActionRemove}
	if _, err := l.Submit(context.Background(), Actor{}, removeSkill); err == nil {
		t.Fatal("SKILL.md deletion must be rejected")
	}
	removeDir := vfs.ChangeSet{Target: "/skills/valid", Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{}}
	if _, err := l.Submit(context.Background(), Actor{}, removeDir); err != nil {
		t.Fatalf("whole skill directory deletion: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "skills", "disabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	disabledPath := filepath.Join(root, "skills", "disabled", "SKILL.md")
	if err := os.WriteFile(disabledPath, disabled, 0o644); err != nil {
		t.Fatal(err)
	}
	removeDisabledSkill := vfs.ChangeSet{Target: "/skills/disabled/SKILL.md", Action: vfs.ChangeActionRemove}
	if _, err := l.Submit(context.Background(), Actor{}, removeDisabledSkill); err == nil {
		t.Fatal("disabled SKILL.md deletion must be rejected")
	}
	removeDisabledDir := vfs.ChangeSet{Target: "/skills/disabled", Action: vfs.ChangeActionRemoveAll, RemoveAll: &vfs.RemoveAllChange{}}
	if _, err := l.Submit(context.Background(), Actor{}, removeDisabledDir); err != nil {
		t.Fatalf("whole disabled skill directory deletion: %v", err)
	}
}

type errorReadFS struct{ error }

func (f errorReadFS) Stat(string) (*vfs.FileInfo, error)     { return nil, f.error }
func (f errorReadFS) ReadDir(string) ([]vfs.FileInfo, error) { return nil, f.error }
func (f errorReadFS) ReadFile(string) ([]byte, error)        { return nil, f.error }

func TestAgentSkillsReadErrorIsLoggedButNotLeaked(t *testing.T) {
	secret := errors.New("backend password=secret")
	var logs bytes.Buffer
	p := newAgentSkills(map[string]config.DocsetSpec{"s": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}, AgentSkills: true}}, errorReadFS{secret}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	cs := vfs.ChangeSet{Target: "/skills/a/note.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("x")}}
	err := p.validateMutation(cs)
	if err == nil || strings.Contains(err.Error(), "password") || !strings.Contains(err.Error(), "unable to validate") {
		t.Fatalf("public error = %v", err)
	}
	if !strings.Contains(logs.String(), "password=secret") || strings.Contains(logs.String(), "x\"") {
		t.Fatalf("log missing backend error or leaked content: %s", logs.String())
	}
}

func TestAgentSkillsMetaCanonicalizesAliases(t *testing.T) {
	docsets := map[string]config.DocsetSpec{
		"skills": {
			Paths:       []config.PathMapping{{Source: "/skills", Display: "/skills"}},
			Aliases:     []string{"/agent-skills"},
			AgentSkills: true,
		},
	}
	canonical := func(p string) string {
		p = vfs.CleanPath(p)
		if p == "/agent-skills" || strings.HasPrefix(p, "/agent-skills/") {
			return "/skills" + strings.TrimPrefix(p, "/agent-skills")
		}
		return p
	}
	p := newAgentSkills(docsets, failingReadFS{err: os.ErrNotExist}, canonical, slog.Default())
	got := p.MetaExtenders()[0]("/agent-skills/deploy/SKILL.md", skillBytes("deploy"), nil)
	if got["agent_skill"] != true {
		t.Fatalf("alias metadata annotation = %v", got)
	}
}

func TestSessionMetaFiltersRequireGrantOnFilterDocset(t *testing.T) {
	docsets := map[string]config.DocsetSpec{
		"skills": {
			Paths:       []config.PathMapping{{Source: "/skills", Display: "/skills"}},
			Access:      config.DocsetAccess{Allow: map[string]string{"skills": "ro"}},
			AgentSkills: true,
		},
		"nested": {
			Paths:  []config.PathMapping{{Source: "/skills/team", Display: "/skills/team"}},
			Access: config.DocsetAccess{Allow: map[string]string{"nested": "ro"}},
		},
	}
	s := &Server{
		auth:         &config.AuthConfig{Docsets: docsets, Roles: map[string]config.RoleSpec{"skills": {}, "nested": {}}},
		authEnforced: true,
		grants:       newGrantRegistry(),
	}
	p := newAgentSkills(docsets, failingReadFS{err: os.ErrNotExist}, s.canonicalPath, slog.Default())
	if err := s.registerPlugin(p); err != nil {
		t.Fatal(err)
	}
	if got := s.sessionMetaFilters(identityWithPolicy("agent", "nested")); len(got) != 0 {
		t.Fatalf("nested ordinary grant exposed ancestor filter: %+v", got)
	}
	got := s.sessionMetaFilters(identityWithPolicy("agent", "skills"))
	if len(got) != 1 || len(got[0].Roots) != 1 || got[0].Roots[0] != "/skills" {
		t.Fatalf("direct skills grant did not bind filter: %+v", got)
	}
}

func TestSessionDocsetsMarksAgentSkillsCanonicalAndAliasRows(t *testing.T) {
	docsets := map[string]config.DocsetSpec{
		"skills": {
			Paths:       []config.PathMapping{{Source: "/skills", Display: "/skills"}},
			Aliases:     []string{"/agent-skills"},
			Access:      config.DocsetAccess{Allow: map[string]string{"reader": "ro"}},
			AgentSkills: true,
		},
	}
	s := &Server{
		auth:         &config.AuthConfig{Docsets: docsets, Roles: map[string]config.RoleSpec{"reader": {}}},
		grants:       newGrantRegistry(),
		authEnforced: true,
		config:       config.Config{Readonly: true},
	}
	rows := s.sessionDocsets(identityWithPolicy("agent", "reader"))
	if len(rows) != 2 || !rows[0].AgentSkills || !rows[1].AgentSkills {
		t.Fatalf("agent skills attributes missing from canonical/alias rows: %+v", rows)
	}
}

type filterPlugin []meta.Filter

func (p filterPlugin) MetaFilters() []meta.Filter { return p }

func TestRegisterPluginRejectsFilterNameAliasCollisions(t *testing.T) {
	permutations := []struct{ first, second meta.Filter }{
		{meta.Filter{Name: "one"}, meta.Filter{Name: "one"}},
		{meta.Filter{Name: "one", Aliases: []string{"alias"}}, meta.Filter{Name: "alias"}},
		{meta.Filter{Name: "one"}, meta.Filter{Name: "two", Aliases: []string{"one"}}},
		{meta.Filter{Name: "one", Aliases: []string{"alias"}}, meta.Filter{Name: "two", Aliases: []string{"alias"}}},
	}
	for _, tt := range permutations {
		s := &Server{grants: newGrantRegistry()}
		if err := s.registerPlugin(filterPlugin{tt.first}); err != nil {
			t.Fatal(err)
		}
		if err := s.registerPlugin(filterPlugin{tt.second}); err == nil {
			t.Fatalf("collision accepted: %+v then %+v", tt.first, tt.second)
		}
	}
	if s := (&Server{grants: newGrantRegistry()}); s.registerPlugin(filterPlugin{{Name: "one", Aliases: []string{"one"}}}) == nil {
		t.Fatal("same-filter collision accepted")
	}
}
