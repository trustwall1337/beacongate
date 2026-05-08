package policy

import "testing"

func TestEngineDefaultAllow(t *testing.T) {
	e := NewEngine()
	d := e.Evaluate("example.com", 80)
	if !d.Allowed {
		t.Fatalf("default should allow")
	}
}

func TestEngineBlockMatches(t *testing.T) {
	e := NewEngine()
	e.Replace([]Rule{mustEnable(Rule{
		ID: "block-tracker", Action: ActionBlock, Match: MatchWildcardHost, Pattern: "*.tracker.example",
	})})
	d := e.Evaluate("a.tracker.example", 6881)
	if d.Allowed || d.RuleID != "block-tracker" {
		t.Fatalf("expected blocked: %+v", d)
	}
}

func TestEngineAllowOverridesBlock(t *testing.T) {
	e := NewEngine()
	e.Replace([]Rule{
		mustEnable(Rule{ID: "allow-corp", Action: ActionAllow, Match: MatchExactHost, Pattern: "corp.example"}),
		mustEnable(Rule{ID: "block-all-example", Action: ActionBlock, Match: MatchWildcardHost, Pattern: "*.example"}),
	})
	d := e.Evaluate("corp.example", 443)
	if !d.Allowed || d.RuleID != "allow-corp" {
		t.Fatalf("expected allow override: %+v", d)
	}
	d = e.Evaluate("other.example", 443)
	if d.Allowed || d.RuleID != "block-all-example" {
		t.Fatalf("expected block fallback: %+v", d)
	}
}

func TestEngineLogOnlyDoesNotBlock(t *testing.T) {
	e := NewEngine()
	e.Replace([]Rule{mustEnable(Rule{
		ID: "log", Action: ActionLogOnly, Match: MatchExactHost, Pattern: "x",
	})})
	d := e.Evaluate("x", 1)
	if !d.Allowed {
		t.Fatalf("log-only should not block")
	}
}

func TestEngineAddRemove(t *testing.T) {
	e := NewEngine()
	e.Add(mustEnable(Rule{ID: "tmp", Action: ActionBlock, Match: MatchExactHost, Pattern: "y"}))
	if d := e.Evaluate("y", 0); d.Allowed {
		t.Fatalf("expected blocked")
	}
	if !e.Remove("tmp") {
		t.Fatalf("remove should report true")
	}
	if e.Remove("tmp") {
		t.Fatalf("second remove should be false")
	}
	if d := e.Evaluate("y", 0); !d.Allowed {
		t.Fatalf("expected allowed after remove")
	}
}
