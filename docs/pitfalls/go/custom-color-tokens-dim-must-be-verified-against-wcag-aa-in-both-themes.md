### Custom color tokens (`--dim`) must be verified against WCAG AA in both themes
- **Saw**: after a UI/UX Pro Max review, the `--dim` color token in both light mode (`#7a7570`) and dark mode (`#6a6a7a`) failed WCAG AA minimum contrast (4.5:1) for `.72rem` labels, despite looking "fine" to the developer
- **Why**: designing by eye is unreliable for low-contrast semantic colors. `--dim` is used for secondary/muted labels at small sizes (`.72rem` ≈ 11.5px), which requires the strictest contrast ratio. Light mode achieved ~4.2:1 (needs 4.5:1), dark mode ~3.8:1 (needs 4.5:1)
- **Fix**: light mode `--dim` → `#635e58` (~5.7:1), dark mode → `#8a8a98` (~5.8:1). Verified with Chrome DevTools contrast checker
- **Lesson**: any custom color token used for text (even "dim" or "muted" text) must be verified against WCAG AA (4.5:1) before shipping. The eye is not a colorimeter — especially for subtle differences around the 4.0–4.5:1 boundary. Always test both light and dark themes independently
- **Refs**: `examples/index.html:105,126`, WCAG 1.4.3
