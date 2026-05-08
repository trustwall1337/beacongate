package policy

import "testing"

func mustEnable(r Rule) Rule { r.Enabled = true; return r }

func TestExactHostMatcher(t *testing.T) {
	r := mustEnable(Rule{ID: "1", Action: ActionBlock, Match: MatchExactHost, Pattern: "Example.com"})
	if !r.Matches("example.com", 0) {
		t.Fatalf("case-insensitive match expected")
	}
	if r.Matches("foo.example.com", 0) {
		t.Fatalf("exact host should not match subdomain")
	}
}

func TestWildcardMatcher(t *testing.T) {
	r := mustEnable(Rule{ID: "1", Action: ActionBlock, Match: MatchWildcardHost, Pattern: "*.example.com"})
	cases := map[string]bool{
		"foo.example.com": true,
		"a.b.example.com": true,
		"example.com":     false, // wildcard requires at least one label
		"notexample.com":  false,
		"badexample.com":  false,
	}
	for h, want := range cases {
		if got := r.Matches(h, 0); got != want {
			t.Fatalf("Matches(%q)=%v want %v", h, got, want)
		}
	}
}

func TestCIDRMatcher(t *testing.T) {
	r := mustEnable(Rule{ID: "1", Action: ActionBlock, Match: MatchCIDR, Pattern: "10.0.0.0/8"})
	if !r.Matches("10.1.2.3", 0) {
		t.Fatalf("expected match in 10/8")
	}
	if r.Matches("11.1.2.3", 0) {
		t.Fatalf("unexpected match outside 10/8")
	}
	if r.Matches("not-an-ip", 0) {
		t.Fatalf("hostname should not match CIDR")
	}
}

func TestPortGate(t *testing.T) {
	r := mustEnable(Rule{ID: "1", Action: ActionBlock, Match: MatchExactHost, Pattern: "x", Port: 443})
	if r.Matches("x", 80) {
		t.Fatalf("port mismatch should not match")
	}
	if !r.Matches("x", 443) {
		t.Fatalf("port match should match")
	}
}
