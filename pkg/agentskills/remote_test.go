package agentskills

import (
	"bytes"
	"testing"
)

var remoteSkill = []byte("---\nname: pdf\ndescription: PDFs\nremote:\n  repo: owner/repo\n  path: skills/pdf\n  ref: main\n  commit: a123456789012345678901234567890123456789\n  kind: tracking\n---\nbody\n")

func TestRemoteHelpersAndSurgicalEdit(t *testing.T) {
	r, ok, err := ReadRemote(remoteSkill)
	if err != nil || !ok || r.Ref != "main" {
		t.Fatalf("remote=%+v ok=%v err=%v", r, ok, err)
	}
	clean, err := StripRemote(remoteSkill)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(clean, []byte("remote:")) {
		t.Fatalf("not stripped: %s", clean)
	}
	status, _ := InjectRemoteStatus(remoteSkill, "offline")
	if !bytes.Contains(status, []byte("remote-status: offline")) {
		t.Fatalf("status missing: %s", status)
	}
	unlinked, allowed, err := SurgicalRemoteEdit(remoteSkill, clean)
	if err != nil || !allowed || !bytes.Equal(unlinked, clean) {
		t.Fatalf("allowed=%v err=%v\n%s", allowed, err, unlinked)
	}
	changed := bytes.Replace(remoteSkill, []byte("body"), []byte("changed"), 1)
	if _, allowed, _ := SurgicalRemoteEdit(remoteSkill, changed); allowed {
		t.Fatal("body edit admitted")
	}
}

func TestValidateRemoteSchema(t *testing.T) {
	if _, err := Validate("pdf", remoteSkill); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Replace(remoteSkill, []byte("commit:"), []byte("unknown:"), 1)
	if _, err := Validate("pdf", bad); err == nil {
		t.Fatal("unknown remote key accepted")
	}
	bad = bytes.Replace(remoteSkill, []byte("remote:"), []byte("remote-status: fake\nremote:"), 1)
	if _, err := Validate("pdf", bad); err == nil {
		t.Fatal("stored remote-status accepted")
	}
	bad = bytes.Replace(remoteSkill, []byte("kind: tracking"), []byte("kind: floating"), 1)
	if _, err := Validate("pdf", bad); err == nil {
		t.Fatal("invalid remote kind accepted")
	}
	bad = bytes.Replace(remoteSkill, []byte("  kind: tracking\n"), nil, 1)
	if _, err := Validate("pdf", bad); err == nil {
		t.Fatal("commit without kind accepted")
	}
}

func TestSurgicalRemoteEditClearsResolvedStateOnTargetChange(t *testing.T) {
	incoming := bytes.Replace(remoteSkill, []byte("ref: main"), []byte("ref: next"), 1)
	normalized, allowed, err := SurgicalRemoteEdit(remoteSkill, incoming)
	if err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	r, ok, err := ReadRemote(normalized)
	if err != nil || !ok || r.Commit != "" || r.Kind != "" {
		t.Fatalf("remote=%+v ok=%v err=%v", r, ok, err)
	}
}

func TestSurgicalRemoteEditRejectsInvalidRemoteBeforeNormalization(t *testing.T) {
	incoming := bytes.Replace(remoteSkill, []byte("  ref: main\n"), []byte("  ref: main\n  unexpected: value\n"), 1)
	if _, allowed, err := SurgicalRemoteEdit(remoteSkill, incoming, "pdf"); err == nil || allowed {
		t.Fatalf("invalid remote admitted: allowed=%v err=%v", allowed, err)
	}
}

func TestInjectStatusPreservesRemoteBytes(t *testing.T) {
	original := []byte("---\nname: pdf\ndescription: PDFs\nremote: { repo: owner/repo, ref: 'main' } # keep\n---\nbody\n")
	got, err := InjectRemoteStatus(original, "offline")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("remote: { repo: owner/repo, ref: 'main' } # keep\n")) {
		t.Fatalf("remote formatting changed:\n%s", got)
	}
}

func TestExtractNameRejectsDisabledTraversalName(t *testing.T) {
	skill := []byte("---\nname: ../escape\nmetadata:\n  agent_skill: disable\n---\n")
	if _, err := ExtractName(skill); err == nil {
		t.Fatal("unsafe disabled name accepted")
	}
}

func TestRemoteEditFailsClosedForFlowFrontmatter(t *testing.T) {
	flow := []byte("---\n{name: pdf, description: PDFs, remote: {repo: owner/repo, ref: main}}\n---\nbody\n")
	if _, err := StripRemote(flow); err == nil {
		t.Fatal("flow-style frontmatter was edited unsafely")
	}
}

func TestRemoteEditPreservesCRLFFraming(t *testing.T) {
	original := []byte("---\r\nname: pdf\r\ndescription: PDFs\r\nremote:\r\n  repo: owner/repo\r\n  ref: main\r\n---\r\nbody\r\n")
	got, err := StripRemote(original)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte("---\r\n")) || !bytes.Contains(got, []byte("\r\n---\r\nbody\r\n")) {
		t.Fatalf("CRLF framing changed:\n%q", got)
	}
}

func TestRemoteEditsPreserveUnrelatedBytes(t *testing.T) {
	original := []byte("---\n# heading\ndescription: 'quoted'\nname: pdf\nremote:\n  repo: owner/repo\n  ref: main\n# keep this comment\nlicense: \"MIT\"\n---\nbody  \n")
	stripped, err := StripRemote(original)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("---\n# heading\ndescription: 'quoted'\nname: pdf\n# keep this comment\nlicense: \"MIT\"\n---\nbody  \n")
	if !bytes.Equal(stripped, want) {
		t.Fatalf("unrelated bytes changed:\n%s", stripped)
	}
}

func TestDisabledSkillStillValidatesReservedFields(t *testing.T) {
	bad := []byte("---\nmetadata:\n  agent_skill: disable\nremote:\n  repo: nope\n  ref: main\n---\n")
	if _, err := Validate("ignored", bad); err == nil {
		t.Fatal("disabled skill bypassed invalid remote")
	}
	bad = []byte("---\nmetadata:\n  agent_skill: disable\nremote-status: fake\n---\n")
	if _, err := Validate("ignored", bad); err == nil {
		t.Fatal("disabled skill bypassed reserved remote-status")
	}
}
