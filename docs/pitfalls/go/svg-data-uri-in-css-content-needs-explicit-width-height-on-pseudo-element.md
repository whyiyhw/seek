### SVG data-URI in CSS `content:` needs explicit width/height on pseudo-element
- **Saw**: replaced `content: "📋"` emoji with an SVG data URI in `::after`, but the icon rendered at 0×0 or default size
- **Why**: `content: url(...)` does not inherit the `font-size` of the element; SVGs render at their intrinsic size (if declared) or 0 if the URI is opaque to the browser. The old emoji sized itself via `font-size: .8rem`, which is meaningless for a `background-image` approach
- **Fix**: switched from `content: url(...)` to `content: ""` + `background-image` + explicit `width`/`height: 16px` + `background-size: contain`. This gives reliable sizing across all browsers
- **Lesson**: when replacing text/emoji in CSS pseudo-elements with SVGs, use the `background-image` pattern (empty content + dimensions + background-size) rather than `content: url(...)`. The former gives you explicit size control; the latter depends on the SVG's intrinsic dimensions and browser implementation
- **Refs**: `examples/index.html:834`, `docs/pitfalls.md:this-commit`
