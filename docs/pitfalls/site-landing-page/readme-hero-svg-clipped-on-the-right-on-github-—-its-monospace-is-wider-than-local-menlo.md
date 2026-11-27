### README hero SVG clipped on the right on GitHub — its monospace is wider than local Menlo
- **Saw**: a hand-authored terminal banner (`examples/hero.svg`) fit when rasterised locally (qlmanage / Menlo) but GitHub's README render clipped the right edge — tagline lost its last word, status strip cut off.
- **Why**: GitHub renders `<text>` with its own wider monospace, so lines sized to just fit the 1200 viewBox locally overflow on GitHub; adding `width`/`height` to a viewBox-only SVG also made `qlmanage` mis-scale, masking the cause.
- **Fix**: keep the SVG viewBox-only (no `width`/`height`) and shorten every text line well under the viewBox width so any monospace fits with margin. `examples/hero.svg`
- **Lesson**: never size SVG `<text>` to just fit against a local rasteriser — the consumer picks a different advance width; leave margin or convert to paths. `qlmanage` is not a faithful preview of GitHub's SVG font.
- **Refs**: `examples/hero.svg`; sibling entry above (pixel block-art portability)
