// Package rules implements declarative, layered content rules.
package rules

import (
	"context"
	"log/slog"

	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type Kind string

const (
	KindRule      Kind = "rule"
	KindHook      Kind = "hook"
	KindOperation Kind = "operation"
)

type Scope string

const (
	ScopeFile   Scope = "file"
	ScopeBundle Scope = "bundle"
)

type ParamType string

const (
	ParamInteger          ParamType = "integer"
	ParamNumber           ParamType = "number"
	ParamString           ParamType = "string"
	ParamBool             ParamType = "boolean"
	ParamIntegerOrInitial ParamType = "integer|initial"
)

type Param struct {
	Name     string
	Type     ParamType
	Required bool
	Default  any
	Doc      string
}

type Manifest struct {
	Path    string
	Kind    Kind
	Scope   Scope
	Summary string
	Doc     string
	Params  []Param
	Example string
}

type RuleSpec struct {
	Match   []string       `yaml:"match" json:"match"`
	Exclude []string       `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Use     string         `yaml:"use" json:"use"`
	With    map[string]any `yaml:"with,omitempty" json:"with,omitempty"`
	Enforce *bool          `yaml:"enforce,omitempty" json:"enforce,omitempty"`
	Default bool           `yaml:"default,omitempty" json:"default,omitempty"`
}

func (s RuleSpec) IsEnforcing() bool { return s.Enforce == nil || *s.Enforce }

type Defaults struct {
	Growth float64
}

type Env struct {
	Defaults Defaults
	Logger   *slog.Logger
}

type Subject struct {
	Mode       Mode
	Path       string
	Dir        string
	Content    []byte
	Existing   func() ([]byte, bool, error)
	Commit     string
	Actor      string
	FS         vfs.FileSystem
	BundleRoot string
	Bundle     *validation.Bundle
}

type Mode int

const (
	ModeAdmit Mode = iota
	ModeValidate
)

type Finding struct {
	Code     string
	Path     string
	Line     int
	Column   int
	Warning  bool
	Measured string
	Limit    string
	Remedy   string
	Override string
}

type Check interface {
	Evaluate(context.Context, Subject) ([]Finding, error)
	OnRemove(context.Context, string) error
	OnMove(context.Context, string, string) error
}

type Member interface {
	Manifest() Manifest
	Compile(with map[string]any, env Env) (Check, error)
}

type Layer struct {
	Origin string
	Scope  string
	Rules  map[string]RuleSpec
}

type LayerSource interface {
	LayersFor(context.Context, string) ([]Layer, error)
}

type CompiledRule struct {
	Name    string
	Spec    RuleSpec
	Origins []string
	Scope   string
	Member  Member
	Check   Check
}
