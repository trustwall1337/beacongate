package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseScriptKeys_StringForm(t *testing.T) {
	got, err := ParseScriptKeys("ID1,ID2,ID3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ScriptKey{
		{ID: "ID1"},
		{ID: "ID2"},
		{ID: "ID3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseScriptKeys_StringFormHandlesWhitespaceAndEmpties(t *testing.T) {
	got, err := ParseScriptKeys("  ID1  , , ID2 ,")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ScriptKey{{ID: "ID1"}, {ID: "ID2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseScriptKeys_ArrayOfStrings(t *testing.T) {
	got, err := ParseScriptKeys([]any{"ID1", "ID2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ScriptKey{{ID: "ID1"}, {ID: "ID2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseScriptKeys_ArrayOfObjects(t *testing.T) {
	got, err := ParseScriptKeys([]any{
		map[string]any{"id": "ID1", "account": "alpha"},
		map[string]any{"id": "ID2", "account": "beta"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ScriptKey{
		{ID: "ID1", Account: "alpha"},
		{ID: "ID2", Account: "beta"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseScriptKeys_MixedEntries(t *testing.T) {
	got, err := ParseScriptKeys([]any{
		map[string]any{"id": "ID1", "account": "alpha"},
		"ID2",
		map[string]any{"id": "ID3"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ScriptKey{
		{ID: "ID1", Account: "alpha"},
		{ID: "ID2"},
		{ID: "ID3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseScriptKeys_EmptyInputsReturnEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"nil", nil},
		{"empty string", ""},
		{"whitespace string", "   "},
		{"empty array", []any{}},
		{"array of empty strings", []any{"", "  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseScriptKeys(tc.in)
			if err != nil {
				t.Errorf("unexpected error for %v: %v", tc.in, err)
			}
			if len(got) != 0 {
				t.Errorf("expected empty slice for %v, got %+v", tc.in, got)
			}
		})
	}
}

func TestParseScriptKeys_ObjectWithoutID(t *testing.T) {
	_, err := ParseScriptKeys([]any{
		map[string]any{"account": "alpha"}, // missing "id"
	})
	if err == nil {
		t.Fatal("expected error for object without id")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error should mention missing id; got %v", err)
	}
}

func TestParseScriptKeys_RejectsUnsupportedType(t *testing.T) {
	_, err := ParseScriptKeys(42)
	if err == nil {
		t.Fatal("expected error for non-string non-array input")
	}
}

func TestParseScriptKeys_RejectsArrayOfWrongType(t *testing.T) {
	_, err := ParseScriptKeys([]any{42, "ID1"})
	if err == nil {
		t.Fatal("expected error for array containing non-string non-object")
	}
}

func TestScriptKeyHelpers(t *testing.T) {
	keys := []ScriptKey{
		{ID: "ID1", Account: "alpha"},
		{ID: "ID2", Account: ""},
		{ID: "ID3", Account: "beta"},
	}
	if got, want := ScriptKeyIDs(keys), []string{"ID1", "ID2", "ID3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ScriptKeyIDs: got %v, want %v", got, want)
	}
	if got, want := ScriptKeyAccounts(keys), []string{"alpha", "", "beta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ScriptKeyAccounts: got %v, want %v", got, want)
	}
}
