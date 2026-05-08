package google

import "strings"

// SanitizeFrontingHost trims whitespace and lower-cases the Host header
// override. It returns the empty string when the input is blank.
func SanitizeFrontingHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.ToLower(s)
}
