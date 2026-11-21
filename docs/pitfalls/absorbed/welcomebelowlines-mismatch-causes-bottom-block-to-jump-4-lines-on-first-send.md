<!-- pitfalls-absorbed: original-section: - -->
### [已吸收] `welcomeBelowLines` mismatch causes bottom block to jump 4 lines on first send
- **Saw**: after pressing Enter to send the first message, the status bar and bottom rule visibly shifted upward by 4 lines relative to their position on the welcome screen. The welcome screen's padding put them just off the bottom of the viewport; the post-send layout put them at the correct terminal bottom, creating a jarring jump
- **Why**: `welcomeBelowLines = 4` (input + status) but the actual rendered bottom block is 7 lines: separator(1) + input(3 + trailing \n = 4) + status(1) + rule(1). The welcome screen formula `height - 14 - 4` under-counted by 3 lines, causing the view to overflow the viewport. The active-chat bottom-pin formula uses dynamic `bottomHeight + 2` which correctly accounts for all components, so the transition between the two padding regimes produced a visible shift
- **Fix**: changed `welcomeBelowLines` from 4 to 7. The padding formula now gives `height - 14 - 7 = height - 21`, which exactly fills the viewport. Updated test expectations (12→9, 42→39). Commit (this one)
- **Lesson**: `welcomeBelowLines` must be manually kept in sync with the actual bottom block components rendered in `View()`. Any addition or removal (separator, rule, textarea height change, extra `\n` after input) requires updating it. The active-chat path avoids this by measuring bottomBuf height dynamically — consider refactoring the welcome path to do the same if bottom components change again
- **Refs**: `internal/tui/banner.go:welcomeBelowLines`, `internal/tui/view.go:View()`, bubbletea textarea `View()` padding loop (always emits `\n` after each of `m.height` lines)

---
- **已吸收**: `welcomeBelowLines` and the welcome-screen padding formula were removed — the banner renders inside View with a width-gated fallback, no pad math → `internal/tui/view.go` welcome-gate block
