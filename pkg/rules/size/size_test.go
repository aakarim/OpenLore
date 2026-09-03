package size

import (
	"context"
	"testing"

	"github.com/aakarim/go-openlore/pkg/rules"
)

func TestLinesFixedMax(t *testing.T) {
	check, err := (Rule{Metric: Lines}).Compile(map[string]any{"max": 3}, rules.Env{})
	if err != nil {
		t.Fatal(err)
	}
	findings, err := check.Evaluate(context.Background(), rules.Subject{Path: "/docs/a.md", Content: []byte("1\n2\n3\n4\n")})
	if err != nil || len(findings) != 1 || findings[0].Code != "size/lines" {
		t.Fatalf("findings=%#v err=%v", findings, err)
	}
}

func TestManifestOnlyAdvertisesFixedMax(t *testing.T) {
	params := (Rule{Metric: Lines}).Manifest().Params
	if len(params) != 1 || params[0].Name != "max" || params[0].Type != rules.ParamInteger || !params[0].Required {
		t.Fatalf("params=%#v, want one required integer max", params)
	}
}
