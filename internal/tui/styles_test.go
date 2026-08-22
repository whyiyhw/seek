package tui

import (
	"image/color"
	"math"
	"testing"

	"charm.land/lipgloss/v2"
)

// The palettes encode a HIERARCHY, not just a set of colours: body text
// carries the message, Muted is everything that must step behind it
// (plan rows, separators, unselected menu items, footer hints), and
// Reasoning sits between the two. That hierarchy is a relationship
// between contrast ratios, and it is invisible in the source — the
// light palette shipped for months with Muted just one grey step off
// body text (237 #3a3a3a against 235 #262626), which reads as a
// perfectly sensible pair of constants in a diff and as a flat,
// undifferentiated wall on screen. These tests measure the
// relationship instead of pinning colour constants, so a future
// re-tint is free to move every value as long as the roles still
// order correctly.

// relLuminance is WCAG 2.x relative luminance. c must be opaque;
// RGBA returns alpha-premultiplied values, which is a no-op at a=1.
func relLuminance(c color.Color) float64 {
	r16, g16, b16, _ := c.RGBA()
	lin := func(v uint32) float64 {
		s := float64(v>>8) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r16) + 0.7152*lin(g16) + 0.0722*lin(b16)
}

// contrast is the WCAG 2.x contrast ratio between two opaque colours.
func contrast(a, b color.Color) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// minRecede is the floor on how much further from the ground body text
// sits than Muted does. The dark palette measures 3.21x; requiring 3.0
// leaves room to re-tint without letting the hierarchy flatten. The
// broken light palette scored 1.33x.
const minRecede = 3.0

func TestPalettes_MutedRecedesBehindBody(t *testing.T) {
	white := lipgloss.Color("#ffffff")
	black := lipgloss.Color("#000000")

	for _, tc := range []struct {
		name   string
		pal    palette
		ground color.Color
		// mutedLighter is the DIRECTION the recede must take: away from
		// body text, toward the ground. On a light ground Muted must be
		// lighter than body; on a dark ground, darker. Note this is NOT
		// what caught the shipped bug — 237 was a hair lighter than 235,
		// i.e. technically the right side — the separation floor below
		// is what caught that. This assertion guards the coarser
		// failure a floor alone would miss: a re-tint that lands Muted
		// on the far side of body text, where it would be both distant
		// from the ground and wrong.
		mutedLighter bool
	}{
		{"dark", darkPalette, black, false},
		{"light", lightPalette, white, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := contrast(tc.pal.Assistant, tc.ground)
			muted := contrast(tc.pal.Muted, tc.ground)
			reasoning := contrast(tc.pal.Reasoning, tc.ground)

			if got := relLuminance(tc.pal.Muted) > relLuminance(tc.pal.Assistant); got != tc.mutedLighter {
				t.Errorf("Muted sits on the wrong side of body text: mutedLighterThanBody=%v, want %v"+
					" (muted L=%.4f, body L=%.4f) — on this ground Muted must move TOWARD the background",
					got, tc.mutedLighter, relLuminance(tc.pal.Muted), relLuminance(tc.pal.Assistant))
			}

			if sep := body / muted; sep < minRecede {
				t.Errorf("hierarchy too flat: body %.2f:1 vs muted %.2f:1 = %.2fx separation, want >= %.1fx",
					body, muted, sep, minRecede)
			}

			// Reasoning is dim but MORE present than chrome — it is the
			// model's thinking, not a hint line. Both palettes encode
			// this today (dark 4.00 vs 3.44; light 4.54 vs 3.45).
			if reasoning <= muted {
				t.Errorf("Reasoning (%.2f:1) must stay more visible than Muted (%.2f:1)", reasoning, muted)
			}
		})
	}
}

// The two themes should recede by comparable amounts — a viewer
// switching themes should not find the chrome noticeably louder in one
// of them. Both Muted values are tuned to ~3.4:1 against their own
// ground; allow a generous band so a re-tint is not blocked by a
// rounding difference.
func TestPalettes_MutedComparableAcrossThemes(t *testing.T) {
	darkMuted := contrast(darkPalette.Muted, lipgloss.Color("#000000"))
	lightMuted := contrast(lightPalette.Muted, lipgloss.Color("#ffffff"))
	ratio := math.Max(darkMuted, lightMuted) / math.Min(darkMuted, lightMuted)
	if ratio > 1.5 {
		t.Errorf("Muted recedes unevenly across themes: dark %.2f:1, light %.2f:1 (%.2fx apart)",
			darkMuted, lightMuted, ratio)
	}
}
