package policy

import (
	"sync"
)

// Decision is the result of evaluating a target against the engine.
type Decision struct {
	Allowed bool
	Action  Action
	RuleID  string
	Reason  string
}

// Engine holds an ordered set of rules. Evaluation walks the rules in order;
// the first applicable rule wins. Allow rules placed before block rules act
// as overrides. Empty engines default to Allow.
type Engine struct {
	mu    sync.RWMutex
	rules []Rule
}

func NewEngine() *Engine { return &Engine{} }

// Replace swaps in a fresh rule list atomically.
func (e *Engine) Replace(rules []Rule) {
	e.mu.Lock()
	e.rules = append([]Rule(nil), rules...)
	e.mu.Unlock()
}

func (e *Engine) Rules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

// Add appends a rule. The caller is responsible for validation.
func (e *Engine) Add(r Rule) {
	e.mu.Lock()
	e.rules = append(e.rules, r)
	e.mu.Unlock()
}

// Remove deletes a rule by ID. It returns true if a rule was removed.
func (e *Engine) Remove(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.rules {
		if r.ID == id {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			return true
		}
	}
	return false
}

// Evaluate decides whether to allow the (host, port) target.
func (e *Engine) Evaluate(host string, port uint16) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.rules {
		if !r.Matches(host, port) {
			continue
		}
		switch r.Action {
		case ActionAllow:
			return Decision{Allowed: true, Action: ActionAllow, RuleID: r.ID, Reason: r.Reason}
		case ActionBlock:
			reason := r.Reason
			if reason == "" {
				reason = "blocked by policy " + r.ID
			}
			return Decision{Allowed: false, Action: ActionBlock, RuleID: r.ID, Reason: reason}
		case ActionLogOnly:
			// log-only rules do not change the decision; continue scanning.
			continue
		}
	}
	return Decision{Allowed: true, Action: ActionAllow, Reason: "default allow"}
}
