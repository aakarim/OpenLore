package agentskills

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	valid := []byte("---\nname: my-skill\ndescription: Does useful work\nmetadata:\n  owner: team\n---\nanything\n")
	if _, err := Validate("my-skill", valid); err != nil {
		t.Fatal(err)
	}
	bad := []byte("---\nname: other\ndescription: ok\nextra: no\n---\n")
	if _, err := Validate("my-skill", bad); err == nil {
		t.Fatal("expected strict validation error")
	}
}

func TestValidateStrictFieldsAndRuneLimits(t *testing.T) {
	base := func(extra string) []byte {
		return []byte("---\nname: my-skill\ndescription: useful\n" + extra + "---\n")
	}
	tests := []struct {
		name string
		body []byte
		bad  bool
	}{
		{"description multibyte boundary", []byte("---\nname: my-skill\ndescription: " + strings.Repeat("界", 1024) + "\n---\n"), false},
		{"description over boundary", []byte("---\nname: my-skill\ndescription: " + strings.Repeat("界", 1025) + "\n---\n"), true},
		{"compatibility multibyte boundary", base("compatibility: " + strings.Repeat("界", 500) + "\n"), false},
		{"compatibility over boundary", base("compatibility: " + strings.Repeat("界", 501) + "\n"), true},
		{"unknown field", base("surprise: true\n"), true},
		{"non-string license", base("license: 7\n"), true},
		{"non-string tools", base("allowed-tools: [cat]\n"), true},
		{"disable model invocation", base("disable-model-invocation: true\n"), false},
		{"non-boolean disable model invocation", base("disable-model-invocation: yes-please\n"), true},
		{"non-map metadata", base("metadata: owner\n"), true},
		{"non-string metadata value", base("metadata:\n  owner: 7\n"), true},
		{"directory mismatch", []byte("---\nname: other\ndescription: useful\n---\n"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Validate("my-skill", tt.body)
			if (err != nil) != tt.bad {
				t.Fatalf("error = %v, bad = %v", err, tt.bad)
			}
		})
	}
}

func TestValidateDisableBeforeStrictValidation(t *testing.T) {
	b := []byte("---\nmetadata:\n  agent_skill: disable\nunknown: allowed-for-disabled\n---\n")
	r, err := Validate("anything", b)
	if err != nil || !r.Disabled {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if _, err := Validate("anything", []byte("not frontmatter")); err == nil {
		t.Fatal("opt-out must remain parseable")
	}
}
