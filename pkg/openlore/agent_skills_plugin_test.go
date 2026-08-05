package openlore

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func skillBytes(name string) []byte {
	return []byte("---\nname: " + name + "\ndescription: useful\n---\n")
}

func markedSkillsFS(t *testing.T) *DirFS {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "valid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "valid", "SKILL.md"), skillBytes("valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDirFS(root, config.FilesConfig{})
	if err := d.SetWriteable(); err != nil {
		t.Fatal(err)
	}
	if err := d.SetXattr("/skills", agentSkillsMarker, nil, 0); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAgentSkillsUsesDynamicMarkersForDiscoveryAndAdmission(t *testing.T) {
	d := markedSkillsFS(t)
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	if got := p.MetaExtenders()[0]("/skills/valid/SKILL.md", skillBytes("valid"), nil); got["agent_skill"] != true {
		t.Fatalf("annotation = %v", got)
	}
	bad := vfs.ChangeSet{Target: "/skills/valid/SKILL.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: skillBytes("wrong")}}
	reached := false
	h := p.WriteMiddleware()[0](func(context.Context, WriteOp) (WriteResult, error) { reached = true; return WriteResult{}, nil })
	if _, err := h(context.Background(), NewWriteOp(Actor{}, bad)); err == nil || reached {
		t.Fatalf("invalid marked write reached terminal: %v", err)
	}
	if err := d.RemoveXattr("/skills", agentSkillsMarker); err != nil {
		t.Fatal(err)
	}
	reached = false
	if _, err := h(context.Background(), NewWriteOp(Actor{}, bad)); err != nil || !reached {
		t.Fatalf("removed marker was not dynamically observed: reached=%v err=%v", reached, err)
	}
}

func TestAgentSkillsDeletionAllowedAndDisabledExcluded(t *testing.T) {
	d := markedSkillsFS(t)
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	if err := p.validateMutation(vfs.ChangeSet{Target: "/skills/valid/SKILL.md", Action: vfs.ChangeActionRemove}); err != nil {
		t.Fatalf("deletion rejected: %v", err)
	}
	disabled := []byte("---\nname: valid\ndescription: useful\nmetadata:\n  agent_skill: disable\n---\n")
	if got := p.MetaExtenders()[0]("/skills/valid/SKILL.md", disabled, nil); got != nil {
		t.Fatalf("disabled annotation = %v", got)
	}
}

func TestAgentSkillsMarkerSetValidatesEntireTreeAtPreApply(t *testing.T) {
	d := markedSkillsFS(t)
	if err := d.RemoveXattr("/skills", agentSkillsMarker); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d.root, "skills", "bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "bad", "SKILL.md"), skillBytes("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	cs := vfs.ChangeSet{Target: "/skills", Action: vfs.ChangeActionSetXattr, Xattr: &vfs.XattrChange{Name: agentSkillsMarker}}
	if err := p.validateMutation(cs); err == nil {
		t.Fatal("invalid recursive collection accepted before marker commit")
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "bad", "SKILL.md"), skillBytes("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.validateMutation(cs); err != nil {
		t.Fatalf("valid recursive collection rejected: %v", err)
	}
}

type filterPlugin []meta.Filter

func (p filterPlugin) MetaFilters() []meta.Filter { return p }

func TestRegisterPluginRejectsFilterNameAliasCollisions(t *testing.T) {
	for _, pair := range [][2]meta.Filter{{{Name: "one"}, {Name: "one"}}, {{Name: "one", Aliases: []string{"alias"}}, {Name: "alias"}}, {{Name: "one"}, {Name: "two", Aliases: []string{"one"}}}} {
		s := &Server{grants: newGrantRegistry()}
		if err := s.registerPlugin(filterPlugin{pair[0]}); err != nil {
			t.Fatal(err)
		}
		if err := s.registerPlugin(filterPlugin{pair[1]}); err == nil {
			t.Fatalf("collision accepted: %+v", pair)
		}
	}
}
