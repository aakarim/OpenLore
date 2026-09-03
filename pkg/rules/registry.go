package rules

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu      sync.RWMutex
	members map[string]Member
}

func NewRegistry() *Registry { return &Registry{members: map[string]Member{}} }

var defaultRegistry = NewRegistry()

func DefaultRegistry() *Registry { return defaultRegistry }
func Register(member Member)     { defaultRegistry.Register(member) }

func (r *Registry) Register(member Member) {
	if member == nil || member.Manifest().Path == "" {
		panic("rules: invalid member")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name := member.Manifest().Path
	if _, exists := r.members[name]; exists {
		panic(fmt.Sprintf("rules: member %q already registered", name))
	}
	r.members[name] = member
}

func (r *Registry) Lookup(name string) (Member, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.members[name]
	return m, ok
}

func (r *Registry) All() []Member {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]Member, 0, len(r.members))
	for _, m := range r.members {
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Manifest().Path < all[j].Manifest().Path })
	return all
}

func (r *Registry) Suggest(name string) string {
	best, score := "", 1<<30
	for _, m := range r.All() {
		if d := distance(name, m.Manifest().Path); d < score {
			best, score = m.Manifest().Path, d
		}
	}
	if score <= 3 {
		return best
	}
	return ""
}

func IsStdlib(name string) bool {
	first := strings.SplitN(name, "/", 2)[0]
	return first != "" && !strings.Contains(first, ".")
}

func distance(a, b string) int {
	d := make([]int, len(b)+1)
	for i := range d {
		d[i] = i
	}
	for i := 1; i <= len(a); i++ {
		prev := d[0]
		d[0] = i
		for j := 1; j <= len(b); j++ {
			old := d[j]
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			d[j] = min(d[j]+1, d[j-1]+1, prev+cost)
			prev = old
		}
	}
	return d[len(b)]
}
