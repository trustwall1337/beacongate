package profiles

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestPathRejectsTraversal ensures the safe-name guard catches
// path-traversal attempts in profile names.
func TestPathRejectsTraversal(t *testing.T) {
	for _, bad := range []string{
		"../etc/passwd",
		"/absolute/path",
		"name with space",
		"name/slash",
		"name\\backslash",
		"",
	} {
		if _, err := Path(bad); err == nil {
			t.Errorf("Path(%q): expected error, got nil", bad)
		}
	}
}

func TestPathAcceptsValidNames(t *testing.T) {
	for _, ok := range []string{
		"work",
		"home",
		"acct-a.backup",
		"alpha_2",
		"v1.1.0",
	} {
		if _, err := Path(ok); err != nil {
			t.Errorf("Path(%q): unexpected error: %v", ok, err)
		}
	}
}

func TestListAndRemove(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := EnsureDir(); err != nil {
		t.Fatal(err)
	}

	dir, _ := Dir()
	if dir != filepath.Join(tmp, "beacongate", "profiles") {
		t.Errorf("dir: got %s", dir)
	}

	for _, name := range []string{"alpha", "beta", "gamma"} {
		p, _ := Path(name)
		if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "beta", "gamma"}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] want %s, got %s", i, want[i], got[i])
		}
	}

	// Idempotent remove.
	if err := Remove("beta"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("beta"); err != nil { // missing file: should be nil
		t.Errorf("idempotent remove: %v", err)
	}

	got, _ = List()
	if len(got) != 2 {
		t.Errorf("after remove: want 2, got %v", got)
	}
}

func TestListMissingDirReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	// Do NOT EnsureDir; directory doesn't exist.
	got, err := List()
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", got)
	}
}
