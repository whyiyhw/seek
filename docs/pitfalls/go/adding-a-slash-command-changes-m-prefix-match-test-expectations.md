### Adding a slash command changes `/m` prefix-match test expectations
- **Saw**: after adding `/memory` command, `TestFilterCommands_PrefixMatch` failed because `/m` prefix matched both `/model` and `/memory` instead of just `/model`
- **Why**: the prefix-match test hardcoded the expected match count and command name. Adding a new command whose prefix overlapped with an existing one naturally expanded the match set
- **Fix**: updated the test expectation from `["/model"]` to `["/model", "/memory"]`
- **Lesson**: tests that assert on exact command-match sets are brittle to new command additions. Either use a unique prefix (`/mo` instead of `/m`) or explicitly document that such tests need updating when commands are added
- **Refs**: `internal/tui/commands_test.go:TestFilterCommands_PrefixMatch`
