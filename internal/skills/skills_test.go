package skills_test

import (
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/assets"
	"github.com/aakarim/go-openlore/internal/skills"
)

func TestEmbeddedDeploymentSkills(t *testing.T) {
	registry := skills.NewRegistry()
	if err := registry.LoadFromFS(assets.Skills()); err != nil {
		t.Fatal(err)
	}

	expected := map[string][]string{
		"setup": {
			"<team-slug>-lore",
			".local/lore.json",
			"/channel/general",
			"Who is this for?",
			"/channel/general/INDEX.md",
			"Do not bake",
			"tokens:",
			"oauth-authorization-server",
			"Require local acceptance",
			"/channel/infrastructure",
			"channel/infrastructure/openlore-server.md",
			`okf_version: "0.2"`,
			"type: Infrastructure",
			"tags: [OpenLore, infrastructure, setup]",
			"author: human:onboarding",
			"generated: { by:",
			"open .local/filesystem/channel/infrastructure/openlore-server.md",
		},
		"onboarding": {
			"before its first deployment",
			".local/filesystem/user/alice/",
			"Verify locally",
			"/channel/infrastructure",
			"# Identities and access",
			"lore validate /channel/infrastructure",
			"open .local/filesystem/channel/infrastructure/openlore-server.md",
		},
		"deploy": {
			"Required outcome specification",
			"/var/lib/openlore/config/openlore.yml",
			"/var/lib/openlore/config/lore.json",
			"Kubernetes ConfigMap",
			"deploy-digitalocean",
			"tokens.issuer",
			"oauth-authorization-server",
			"Required production acceptance",
			"Record the deployment",
			"/channel/infrastructure/openlore-server.md",
			"status: stable",
			"verified: { by:",
			"/lore/channel/infrastructure/openlore-server.md",
		},
		"upgrade": {
			"root `openlore.yml`",
			"FROM ghcr.io/aakarim/openlore:1.2.3",
			"Do not build, test",
		},
	}
	for _, provider := range []string{"fly", "railway", "aws", "gcp", "azure", "digitalocean"} {
		expected["deploy-"+provider] = []string{
			"openlore.yml",
			"/var/lib/openlore",
			".local/lore.json",
			"./out --config /var/lib/openlore/config/openlore.yml",
			"image digest",
		}
	}

	for name, snippets := range expected {
		skill, ok := registry.Get(name)
		if !ok {
			t.Errorf("embedded skill %q is not registered", name)
			continue
		}
		if skill.Description == "" {
			t.Errorf("embedded skill %q has no description", name)
		}
		for _, snippet := range snippets {
			if !strings.Contains(strings.ToLower(skill.Content), strings.ToLower(snippet)) {
				t.Errorf("embedded skill %q does not contain %q", name, snippet)
			}
		}
	}
}
