package openlore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/agentskills"
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/skillsremote"
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

func TestAgentSkillsDefaultRemoteClientRejectsPrivateHosts(t *testing.T) {
	d := markedSkillsFS(t)
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	transport, ok := p.remote.HTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport %T", p.remote.HTTP.Transport)
	}
	if _, err := transport.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "443")); err == nil || !strings.Contains(err.Error(), "no public IP") {
		t.Fatalf("default sync client allowed private host: %v", err)
	}
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
	if err := p.validateMutation(Actor{}, vfs.ChangeSet{Target: "/skills/valid/SKILL.md", Action: vfs.ChangeActionRemove}); err != nil {
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
	if err := p.validateMutation(Actor{}, cs); err == nil {
		t.Fatal("invalid recursive collection accepted before marker commit")
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "bad", "SKILL.md"), skillBytes("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.validateMutation(Actor{}, cs); err != nil {
		t.Fatalf("valid recursive collection rejected: %v", err)
	}
}

func TestAgentSkillsNormalizesSurgicalRemoteEditBeforeAdmission(t *testing.T) {
	d := markedSkillsFS(t)
	stored, err := agentskills.GraftRemote(skillBytes("valid"), agentskills.Remote{
		Repo: "owner/repo", Ref: "main", Commit: strings.Repeat("a", 40), Kind: "tracking",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
		t.Fatal(err)
	}
	incoming, err := agentskills.GraftRemote(stored, agentskills.Remote{
		Repo: "owner/repo", Ref: "next", Commit: strings.Repeat("a", 40), Kind: "tracking",
	})
	if err != nil {
		t.Fatal(err)
	}
	incoming, err = agentskills.InjectRemoteStatus(incoming, "offline")
	if err != nil {
		t.Fatal(err)
	}
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	var committed []byte
	h := p.WriteMiddleware()[0](func(_ context.Context, op WriteOp) (WriteResult, error) {
		committed = op.Leaves()[0].Write.Bytes
		return WriteResult{}, nil
	})
	cs := vfs.ChangeSet{Target: "/skills/valid/SKILL.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: incoming}}
	if _, err := h(context.Background(), NewWriteOp(Actor{}, cs)); err != nil {
		t.Fatal(err)
	}
	remote, linked, err := agentskills.ReadRemote(committed)
	if err != nil || !linked || remote.Ref != "next" || remote.Commit != "" || remote.Kind != "" {
		t.Fatalf("normalized remote=%+v linked=%v err=%v", remote, linked, err)
	}
	if bytes.Contains(committed, []byte("remote-status")) {
		t.Fatalf("remote-status persisted: %s", committed)
	}
}

func TestAgentSkillsRejectsInvalidSurgicalRemote(t *testing.T) {
	d := markedSkillsFS(t)
	stored, err := agentskills.GraftRemote(skillBytes("valid"), agentskills.Remote{Repo: "owner/repo", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
		t.Fatal(err)
	}
	incoming := bytes.Replace(stored, []byte("  ref: main\n"), []byte("  ref: main\n  unexpected: value\n"), 1)
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	reached := false
	h := p.WriteMiddleware()[0](func(_ context.Context, _ WriteOp) (WriteResult, error) {
		reached = true
		return WriteResult{}, nil
	})
	cs := vfs.ChangeSet{Target: "/skills/valid/SKILL.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: incoming}}
	if _, err := h(context.Background(), NewWriteOp(Actor{}, cs)); err == nil || reached {
		t.Fatalf("invalid remote reached terminal: reached=%v err=%v", reached, err)
	}
}

func TestAgentSkillsLinkedProtectionRequiresEffectiveCollection(t *testing.T) {
	d := markedSkillsFS(t)
	stored, err := agentskills.GraftRemote(skillBytes("valid"), agentskills.Remote{Repo: "owner/repo", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveXattr("/skills", agentSkillsMarker); err != nil {
		t.Fatal(err)
	}
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	cs := vfs.ChangeSet{Target: "/skills/valid/local.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("local")}}
	reached := false
	h := p.WriteMiddleware()[0](func(_ context.Context, _ WriteOp) (WriteResult, error) {
		reached = true
		return WriteResult{}, nil
	})
	if _, err := h(context.Background(), NewWriteOp(Actor{}, cs)); err != nil || !reached {
		t.Fatalf("ineffective linked skill stayed protected: reached=%v err=%v", reached, err)
	}
}

func TestAgentSkillsEmptyChangesSingletonStaysValid(t *testing.T) {
	d := markedSkillsFS(t)
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	cs := vfs.ChangeSet{Target: "/skills/valid/SKILL.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: skillBytes("valid")}, Changes: []vfs.Change{}}
	h := p.WriteMiddleware()[0](func(_ context.Context, op WriteOp) (WriteResult, error) {
		return WriteResult{}, vfs.ValidateChangeSet(op.persistenceChangeSet())
	})
	if _, err := h(context.Background(), NewWriteOp(Actor{}, cs)); err != nil {
		t.Fatalf("singleton with empty changes rejected: %v", err)
	}
}

func remoteSyncClient(t *testing.T, files map[string]string) (*skillsremote.Client, string) {
	t.Helper()
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/info/refs", func(w http.ResponseWriter, _ *http.Request) {
		packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
		fmt.Fprint(w, packet("# service=git-upload-pack\n")+"0000"+packet(newSHA+" HEAD\x00symref=HEAD:refs/heads/main\n")+packet(newSHA+" refs/heads/main\n")+"0000")
	})
	mux.HandleFunc("/owner/repo/tar.gz/"+newSHA, func(w http.ResponseWriter, _ *http.Request) {
		gz := gzip.NewWriter(w)
		archive := tar.NewWriter(gz)
		for name, content := range files {
			fullName := "repo-x/valid/" + name
			_ = archive.WriteHeader(&tar.Header{Name: fullName, Size: int64(len(content)), Typeflag: tar.TypeReg})
			_, _ = archive.Write([]byte(content))
		}
		_ = archive.Close()
		_ = gz.Close()
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &skillsremote.Client{HTTP: server.Client(), GitHubBase: server.URL, CodeloadBase: server.URL}, oldSHA
}

func TestAgentSkillsSyncHandlesFileDirectoryTransitions(t *testing.T) {
	for _, tc := range []struct {
		name, oldKind string
		remoteFiles   map[string]string
		wantDir       bool
	}{
		{name: "file to directory", oldKind: "file", remoteFiles: map[string]string{"SKILL.md": string(skillBytes("valid")), "assets.md/icon.md": "icon"}, wantDir: true},
		{name: "directory to file", oldKind: "dir", remoteFiles: map[string]string{"SKILL.md": string(skillBytes("valid")), "assets.md": "asset"}, wantDir: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := markedSkillsFS(t)
			target := filepath.Join(d.root, "skills", "valid", "assets.md")
			if tc.oldKind == "file" {
				if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "old.md"), []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			client, oldSHA := remoteSyncClient(t, tc.remoteFiles)
			stored, err := agentskills.GraftRemote(skillBytes("valid"), agentskills.Remote{Repo: "owner/repo", Path: "valid", Ref: "main", Commit: oldSHA, Kind: "tracking"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
				t.Fatal(err)
			}
			p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
			p.remote = client
			p.submit = func(_ context.Context, cs vfs.ChangeSet) error {
				_, err := vfs.CommitChangeSet(d, cs)
				return err
			}
			p.syncSkill(context.Background(), "/skills/valid", true)
			info, err := d.Stat("/skills/valid/assets.md")
			if err != nil || info.IsDir() != tc.wantDir {
				t.Fatalf("transition result: info=%+v err=%v", info, err)
			}
		})
	}
}

func TestAgentSkillsSyncNormalizesUpstreamAndCanonicalizesRemote(t *testing.T) {
	d := markedSkillsFS(t)
	client, oldSHA := remoteSyncClient(t, map[string]string{
		"SKILL.md": "---\nname: Upstream Display Name\ndescription: useful\nargument-hint: 3\n---\n",
	})
	stored, err := agentskills.GraftRemote(skillBytes("valid"), agentskills.Remote{Repo: "owner/repo", Path: "valid", Ref: "main", Commit: oldSHA, Kind: "tracking"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	p.remote = client
	p.submit = func(_ context.Context, cs vfs.ChangeSet) error {
		_, err := vfs.CommitChangeSet(d, cs)
		return err
	}
	p.syncSkill(context.Background(), "/skills/valid", true)
	got, err := d.ReadFile("/skills/valid/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: valid", "display-name: Upstream Display Name", "argument-hint: \"3\"", "repo: https://github.com/owner/repo"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if _, err := agentskills.Validate("valid", got); err != nil {
		t.Fatalf("synced skill invalid: %v", err)
	}
}

func TestAgentSkillsPreApplyBypassIsActorScoped(t *testing.T) {
	d := markedSkillsFS(t)
	stored, err := agentskills.GraftRemote(skillBytes("valid"), agentskills.Remote{Repo: "owner/repo", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	change := vfs.ChangeSet{Target: "/skills/valid/local.md", Action: vfs.ChangeActionRemove}
	if err := p.validateMutation(Actor{ID: "user"}, change); err == nil {
		t.Fatal("user mutation bypassed linked-skill protection")
	}
	if err := p.validateMutation(Actor{ID: "agent_skills_remote", internal: true}, change); err != nil {
		t.Fatalf("internal actor was not trusted: %v", err)
	}
	if err := p.validateMutation(Actor{ID: "agent_skills_remote"}, change); err == nil {
		t.Fatal("spoofed internal actor ID bypassed protection")
	}
}

func TestAgentSkillsStaleFailureDoesNotFollowChangedRemote(t *testing.T) {
	d := markedSkillsFS(t)
	remote := agentskills.Remote{Repo: "owner/new", Ref: "v1", Commit: strings.Repeat("c", 40), Kind: "pinned"}
	stored, err := agentskills.GraftRemote(skillBytes("valid"), remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	p.submit = func(_ context.Context, cs vfs.ChangeSet) error {
		_, err := vfs.CommitChangeSet(d, cs)
		return err
	}
	p.failures["/skills/valid"] = remoteState{fingerprint: "old-link", message: "offline"}
	p.syncSkill(context.Background(), "/skills/./valid", false)
	if _, exists := p.failures["/skills/valid"]; exists {
		t.Fatal("stale failure survived canonical pinned read")
	}
	got := p.ContentTransforms()[0]("/skills/valid/SKILL.md", stored)
	if bytes.Contains(got, []byte("remote-status")) {
		t.Fatalf("stale status injected:\n%s", got)
	}
}

func TestAgentSkillsCanonicalizationFailureIsReported(t *testing.T) {
	d := markedSkillsFS(t)
	stored, err := agentskills.GraftRemote(skillBytes("valid"), agentskills.Remote{Repo: "owner/repo", Ref: "main", Commit: strings.Repeat("c", 40), Kind: "tracking"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "skills", "valid", "SKILL.md"), stored, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newAgentSkills(map[string]config.DocsetSpec{"skills": {Paths: []config.PathMapping{{Source: "/skills", Display: "/skills"}}}}, d, nil, slog.Default())
	p.submit = func(context.Context, vfs.ChangeSet) error { return errors.New("concurrent edit") }
	_, _, err = p.updateRemoteSkill(context.Background(), "/skills/valid")
	if err == nil || !strings.Contains(err.Error(), "canonicalization failed") {
		t.Fatalf("canonicalization failure not reported: %v", err)
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
