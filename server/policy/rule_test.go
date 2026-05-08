package policy

import (
	"errors"
	"testing"
	"time"
)

func TestRuleValidate(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{name: "exact-host ok", rule: Rule{ID: "1", Action: ActionBlock, Match: MatchExactHost, Pattern: "example.com"}},
		{name: "wildcard-host ok", rule: Rule{ID: "2", Action: ActionBlock, Match: MatchWildcardHost, Pattern: "*.example.com"}},
		{name: "exact-ip ok", rule: Rule{ID: "3", Action: ActionAllow, Match: MatchExactIP, Pattern: "10.0.0.1"}},
		{name: "cidr ok", rule: Rule{ID: "4", Action: ActionBlock, Match: MatchCIDR, Pattern: "10.0.0.0/8"}},
		{name: "missing id", rule: Rule{Action: ActionBlock, Match: MatchExactHost, Pattern: "x"}, wantErr: true},
		{name: "bad action", rule: Rule{ID: "5", Action: "weird", Match: MatchExactHost, Pattern: "x"}, wantErr: true},
		{name: "bad cidr", rule: Rule{ID: "6", Action: ActionBlock, Match: MatchCIDR, Pattern: "not-a-cidr"}, wantErr: true},
		{name: "bad ip", rule: Rule{ID: "7", Action: ActionBlock, Match: MatchExactIP, Pattern: "not-an-ip"}, wantErr: true},
		{name: "bare wildcard", rule: Rule{ID: "8", Action: ActionBlock, Match: MatchWildcardHost, Pattern: "*"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rule.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidRule) {
				t.Fatalf("expected ErrInvalidRule, got %v", err)
			}
		})
	}
}

func TestRuleEnabledFlag(t *testing.T) {
	r := Rule{ID: "x", Enabled: false, Action: ActionBlock, Match: MatchExactHost, Pattern: "example.com"}
	if r.Matches("example.com", 0) {
		t.Fatalf("disabled rule should not match")
	}
	r.Enabled = true
	if !r.Matches("example.com", 0) {
		t.Fatalf("enabled rule should match")
	}
}

func TestBaselineValid(t *testing.T) {
	for _, r := range Baseline() {
		if err := r.Validate(); err != nil {
			t.Fatalf("baseline rule %s invalid: %v", r.ID, err)
		}
	}
	if len(Baseline()) == 0 {
		t.Fatalf("baseline should not be empty")
	}
	// timestamps are populated
	for _, r := range Baseline() {
		if r.UpdatedAt.IsZero() {
			t.Fatalf("baseline rule %s has zero UpdatedAt", r.ID)
		}
		if time.Since(r.UpdatedAt) > time.Minute {
			t.Fatalf("baseline rule %s timestamp drift", r.ID)
		}
	}
}
