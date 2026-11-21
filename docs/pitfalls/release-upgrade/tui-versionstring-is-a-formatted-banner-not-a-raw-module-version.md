### `tui.VersionString()` is a formatted banner, not a raw module version
- **Saw**: building `seek -upgrade` against the embedded version, the dev-build refusal check (`IsDev("dev") == true`) never fired — the upgrader happily proceeded to fetch releases on a local `go build`
- **Why**: `tui.VersionString()` returns the *display* form (`"dev · f68f4fd+"`), not the raw module version (`"dev"`). Pasting that into `IsDev` and `compareSemver` looked sensible but quietly compared the wrong string — only the version token before the `" · "` separator is the real version
- **Fix**: introduced `coreVersion()` in `internal/upgrade/version.go` that strips the `" · <rev>[+]"` suffix; both `IsDev` and `splitSemver` route through it. Added tests for the formatted form
- **Lesson**: any helper named `VersionString` is a user-facing display string by default; the raw value is almost always elsewhere. Treat it as opaque text and re-parse, don't assume "version == raw version"
