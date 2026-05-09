package appsscript

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestLintNoQuotaPollingOnRequestPath enforces Workstream A9 invariant
// #4: the request hot path (Roundtrip → attempt) MUST NOT call any
// quota-polling, stats-fetching, or HTTP-blocking quota function.
// `bumpDailyCount` is allowed (it's a single-mutex increment, not a
// poll); blocking quota work belongs in the dedicated background
// loop only.
//
// This is a static parser-based check, not a runtime instrumentation.
// Static is preferable here because a runtime check could be defeated
// by a code path that only triggers under specific conditions; the
// static check covers every code path including unreachable ones.
func TestLintNoQuotaPollingOnRequestPath(t *testing.T) {
	hotPathFunctions := map[string]struct{}{
		"Roundtrip": {},
		"attempt":   {},
	}
	forbiddenCalls := map[string]string{
		"runQuotaPollLoop":        "blocking quota poll loop",
		"pollAllDeployments":      "synchronous quota fetch across all deployments",
		"pollOneDeployment":       "synchronous quota fetch for one deployment",
		"recordScriptStatsLocked": "stats parser (called only by pollOneDeployment)",
		"startQuotaLoop":          "starts the quota goroutine — should be in New(), not Roundtrip",
	}

	fset := token.NewFileSet()
	// parser.ParseDir is marked deprecated in Go 1.25 because it
	// doesn't honor build tags. We don't have build-tagged files in
	// this package, so the simpler API is fine and pulling in
	// golang.org/x/tools/go/packages just for a lint test is
	// overkill.
	pkg, err := parser.ParseDir(fset, ".", nil, parser.ParseComments) //nolint:staticcheck // SA1019 — see comment above
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	for _, p := range pkg {
		for fname, file := range p.Files {
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Name == nil {
					return true
				}
				if _, want := hotPathFunctions[fn.Name.Name]; !want {
					return true
				}
				ast.Inspect(fn.Body, func(inner ast.Node) bool {
					call, ok := inner.(*ast.CallExpr)
					if !ok {
						return true
					}
					var ident string
					switch fun := call.Fun.(type) {
					case *ast.Ident:
						ident = fun.Name
					case *ast.SelectorExpr:
						ident = fun.Sel.Name
					}
					if reason, forbidden := forbiddenCalls[ident]; forbidden {
						t.Errorf("A9 #4 violation: %s contains call to %s — %s; quota work must run on the dedicated background goroutine, not the request hot path",
							fn.Name.Name, ident, reason)
					}
					return true
				})
				return false // don't recurse into nested funcs of the hot-path func; we already inspected its body
			})
		}
	}
}

// TestLintTLSMinVersionIsTLS13 enforces Workstream A9 corollary:
// every TLS handshake the appsscript transport opens must be TLS 1.3
// minimum. Downgrade attempts are a fingerprintable network-shape
// signal that breaks the disguise.
func TestLintTLSMinVersionIsTLS13(t *testing.T) {
	const wantMinVersion = "VersionTLS13"
	fset := token.NewFileSet()
	// parser.ParseDir is marked deprecated in Go 1.25 because it
	// doesn't honor build tags. We don't have build-tagged files in
	// this package, so the simpler API is fine and pulling in
	// golang.org/x/tools/go/packages just for a lint test is
	// overkill.
	pkg, err := parser.ParseDir(fset, ".", nil, parser.ParseComments) //nolint:staticcheck // SA1019 — see comment above
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	foundTLSConfig := false
	for _, p := range pkg {
		for fname, file := range p.Files {
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				comp, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := comp.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkgIdent, ok := sel.X.(*ast.Ident); !ok || pkgIdent.Name != "tls" || sel.Sel.Name != "Config" {
					return true
				}
				foundTLSConfig = true
				// Walk the Config literal's keyed elements and find
				// MinVersion. If absent or wrong, fail.
				var hasCorrectMinVersion bool
				for _, elt := range comp.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "MinVersion" {
						continue
					}
					if val, ok := kv.Value.(*ast.SelectorExpr); ok && val.Sel.Name == wantMinVersion {
						hasCorrectMinVersion = true
					}
				}
				if !hasCorrectMinVersion {
					t.Errorf("A9 corollary violation in %s: tls.Config without MinVersion=tls.%s — TLS 1.3 minimum is part of the disguise",
						fname, wantMinVersion)
				}
				return true
			})
		}
	}
	if !foundTLSConfig {
		t.Fatal("expected at least one tls.Config literal in the appsscript package; lint may be misconfigured")
	}
}
