// Package normalize provides small string normalizers.
package normalize

import "strings"

// KeepAlpha returns s with every non-ASCII-letter rune removed.
func KeepAlpha(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LowerASCII returns s lowercased, but only ASCII letters — any other
// rune is passed through unchanged.
func LowerASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
