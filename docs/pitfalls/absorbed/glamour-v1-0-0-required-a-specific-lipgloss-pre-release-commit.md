<!-- pitfalls-absorbed: original-section: - -->
### [已吸收] `glamour@v1.0.0` required a specific `lipgloss` pre-release commit
- **Saw**: `go get` for all four charm packages at once errored: glamour wanted `v1.1.1-0.20250404203927-76690c660834`, not the latest `v1.1.0`
- **Why**: glamour's `go.mod` pinned a development version of lipgloss with API changes that hadn't shipped in a tagged release
- **Fix**: pull lipgloss at the exact required pseudo-version first, then everything else. Commit `9be599b`
- **Lesson**: when fetching a family of related packages, do them one at a time so version conflicts surface with names attached, not as a confusing batch error
- **已吸收**: the v1 pseudo-version pin chain ended with the v2 migration — go.mod now pins tagged `charm.land/glamour/v2 v2.0.1` + `lipgloss/v2 v2.0.6` → `go.mod`; commit `ba94aea`
