package normalize

import "testing"

func TestKeepAlpha(t *testing.T) {
	cases := map[string]string{
		"a1b2c3":     "abc",
		"HELLO-World": "HELLOWorld",
		"你好ok":        "ok",
		"":           "",
		"123!!":      "",
	}
	for in, want := range cases {
		if got := KeepAlpha(in); got != want {
			t.Errorf("KeepAlpha(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLowerASCII(t *testing.T) {
	cases := map[string]string{
		"AbC":    "abc",
		"Ünïcode": "Ünïcode",
		"mixed42": "mixed42",
	}
	for in, want := range cases {
		if got := LowerASCII(in); got != want {
			t.Errorf("LowerASCII(%q) = %q, want %q", in, got, want)
		}
	}
}
