package policy

import (
	"path/filepath"
	"testing"
)

func TestFileStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	fs, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.List()) != 0 {
		t.Fatalf("empty store expected")
	}
	r := mustEnable(Rule{ID: "rule-a", Action: ActionBlock, Match: MatchExactHost, Pattern: "x.example"})
	if err := fs.Upsert(r); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, ok := fs.Get("rule-a"); !ok || got.Pattern != "x.example" {
		t.Fatalf("get: %+v ok=%v", got, ok)
	}
	r.Pattern = "y.example"
	if err := fs.Upsert(r); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := fs.Get("rule-a")
	if got.Pattern != "y.example" {
		t.Fatalf("expected updated pattern")
	}

	// Reopen and verify persistence.
	fs2, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := fs2.Get("rule-a"); !ok || got.Pattern != "y.example" {
		t.Fatalf("persistence broken: %+v ok=%v", got, ok)
	}

	deleted, err := fs2.Delete("rule-a")
	if err != nil || !deleted {
		t.Fatalf("delete: %v deleted=%v", err, deleted)
	}
	if _, ok := fs2.Get("rule-a"); ok {
		t.Fatalf("should be gone")
	}
}

func TestFileStoreReplaceValidates(t *testing.T) {
	fs, err := OpenFileStore(filepath.Join(t.TempDir(), "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = fs.Replace([]Rule{{ID: "", Action: ActionBlock, Match: MatchExactHost, Pattern: "x"}})
	if err == nil {
		t.Fatalf("expected validation error")
	}
}
