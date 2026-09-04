package rules

import (
	"path"
	"strings"
)

func Matches(spec RuleSpec, rel string) bool {
	rel = strings.Trim(strings.ReplaceAll(rel, "\\", "/"), "/")
	matched := false
	for _, pattern := range spec.Match {
		if glob(pattern, rel) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, pattern := range spec.Exclude {
		if glob(pattern, rel) {
			return false
		}
	}
	return true
}

func glob(pattern, name string) bool {
	p, n := split(pattern), split(name)
	var walk func(int, int) bool
	walk = func(i, j int) bool {
		if i == len(p) {
			return j == len(n)
		}
		if p[i] == "**" {
			return walk(i+1, j) || (j < len(n) && walk(i, j+1))
		}
		if j == len(n) {
			return false
		}
		ok, err := path.Match(p[i], n[j])
		return err == nil && ok && walk(i+1, j+1)
	}
	return walk(0, 0)
}

func split(s string) []string {
	s = strings.Trim(strings.ReplaceAll(s, "\\", "/"), "/")
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}
