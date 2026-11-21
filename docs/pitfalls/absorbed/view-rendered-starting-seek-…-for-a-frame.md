<!-- pitfalls-absorbed: original-section: - -->
### [已吸收] `View()` rendered "starting seek …" for a frame
- **Saw**: a brief flash of the literal string "starting seek …" on launch
- **Why**: `View()` returned a placeholder string when `m.ready == false` (i.e. before the first WindowSizeMsg); even the synthetic one needs one Update cycle
- **Fix**: render the welcome banner in the not-ready branch instead of the placeholder — same content the viewport will hold once we know the dimensions, so no visual jump. Commit `766a045`
- **Lesson**: a "loading" message that shows for less than one frame should just be a softer first frame, not a string change
- **已吸收**: the "render the banner in the not-ready branch" fix is not what ships — the !ready branch deliberately shows a minimal i18n hint (`view.starting`); the banner renders inside View once width is known → `internal/tui/view.go` (`!m.ready` branch)
