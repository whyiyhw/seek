### Injecting filesystem failure for cross-OS tests: chmod lies, file-as-dir is honest
- **Saw**: (design-time, M-B1) testing "a failed skill write leaves no partial state" wanted a read-only parent dir (`chmod 0500`) so `MkdirAll`/`WriteFile` would fail. On Windows the directory read-only bit doesn't block `mkdir` (chmod applies to files only), so the injection silently succeeds and the test fails on the third CI OS.
- **Why**: Go's `os.Chmod` on Windows toggles the FILE read-only attribute; directories keep their ACLs. Related trap: `stat("<file>/child")` returns `ENOTDIR` on Unix, `ENOENT` on Windows — even asserting *which* error branch fired isn't portable.
- **Fix**: inject a regular FILE where a directory must go (e.g. `<proj>/.seek` as a file) and assert the CONTRACT (error + zero residue via a walk), never the branch. `internal/skillmgr/writeproject_test.go` `TestWriteProjectSkill_FailureLeavesNoPartialState`.
- **Lesson**: on a three-OS CI, failure injection must fail for the same reason everywhere; when it can't, assert the invariant ("this branch errored" → "error out, nothing written").
- **Refs**: `internal/skillmgr/writeproject_test.go`, `.github/workflows/ci.yml`
