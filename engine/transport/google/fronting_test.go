package google

import "testing"

func TestSanitizeFrontingHost(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{" ", ""},
		{"Foo.Example.com", "foo.example.com"},
		{"  Bar.example  ", "bar.example"},
	}
	for _, tc := range tests {
		if got := SanitizeFrontingHost(tc.in); got != tc.want {
			t.Fatalf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
