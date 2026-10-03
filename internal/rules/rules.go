// Package rules implements versioned, deterministic rule sets. A match binds
// to the rule version that was active when it started; later rule upgrades
// never rewrite ongoing or finished matches.
package rules

import (
	"sync"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// RuleContext carries everything a rule needs to evaluate one operation.
type RuleContext struct {
	Match      *model.MatchState
	Player     *model.PlayerState
	Op         *model.Operation
	ReceivedAt int64 // server time the operation was received
	Config     *config.Config
}

// Rule evaluates one operation and returns zero or more findings. Rules must
// be pure functions of the context so that replay reproduces identical
// verdicts.
type Rule interface {
	ID() string
	Evaluate(ctx *RuleContext) []model.Finding
}

// RuleSet is an immutable, versioned collection of rules.
type RuleSet struct {
	Version string
	Rules   []Rule
}

// Registry holds all known rule set versions and the currently active one.
type Registry struct {
	mu      sync.RWMutex
	sets    map[string]*RuleSet
	current string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{sets: make(map[string]*RuleSet)}
}

// Register adds (or replaces) a rule set version.
func (r *Registry) Register(rs *RuleSet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sets[rs.Version] = rs
}

// Get returns the rule set for a version.
func (r *Registry) Get(version string) (*RuleSet, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rs, ok := r.sets[version]
	return rs, ok
}

// SetCurrent activates a rule version for new matches.
func (r *Registry) SetCurrent(version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sets[version]; !ok {
		return ErrUnknownVersion
	}
	r.current = version
	return nil
}

// Current returns the active rule set.
func (r *Registry) Current() *RuleSet {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sets[r.current]
}

// Evaluate runs every rule in the set against the context.
func (rs *RuleSet) Evaluate(ctx *RuleContext) []model.Finding {
	var out []model.Finding
	for _, rule := range rs.Rules {
		out = append(out, rule.Evaluate(ctx)...)
	}
	return out
}
