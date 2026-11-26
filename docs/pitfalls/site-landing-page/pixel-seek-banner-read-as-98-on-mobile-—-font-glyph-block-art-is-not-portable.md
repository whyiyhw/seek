### Pixel "SEEK" banner read as "98%" on mobile — font-glyph block-art is not portable
- **Saw**: hero wordmark built from a `<pre>` of `█` (U+2588) rendered "SEEK" on desktop but sheared into "98%" in a mobile CJK webview — S→9, E E→8 8, K→%.
- **Why**: the grid holds only while `█` fills exactly one cell; mobile lacks JetBrains Mono / SF Mono / Fira Code / Courier New, so `█` falls back to a CJK/proportional glyph at another advance width and `line-height: 1.15` adds gutters.
- **Fix**: render the wordmark as DOM cells on a 23×7 CSS grid (`<i style="grid-area:r/c">` per lit pixel), font-independent; source pattern kept as an HTML comment. `examples/index.html` `.pixel-logo`
- **Lesson**: never render a wordmark as font-glyph block art (█, box-drawing, braille) — CJK webviews fall back to wrong-width glyphs; use DOM/SVG rects, and when a visual bug survives a fix check the layer (medium vs letterforms). Flat squares + uniform gutter read pixel-art; per-pixel rounding/gradients read "LED display".
- **Refs**: `examples/index.html` `.pixel-logo` + pixel-grid markup; commits c346c20, 85efd29 (the two mis-aimed attempts)
