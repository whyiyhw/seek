### Flipping loadSession on exposes every id ever handed out under the old scheme
- **Saw**: first real "continue conversation" after session/load shipped failed with `session: open ~/.seek/sessions/seek.jsonl: no such file or directory` — paseo had stored the PRE-upgrade sessionId (the project directory name "seek"), and the raw ENOENT reached the client.
- **Why**: clients persist the ids the agent hands out across upgrades; `loadSession:true` makes every historical id loadable-in-principle, but ids from the project-name era index no transcript (the ACP lane didn't persist then), so those loads can never succeed.
- **Fix**: `acpBackend.LoadSession` maps `os.ErrNotExist` to guidance naming the id and the era ("sessions created before session/load shipped … start a new conversation; it will resume fine"); other load errors keep the wrapped form.
- **Lesson**: when a protocol capability flips on, enumerate what OLD handles do against the new path — the migration surface is every id ever issued.
- **Refs**: `cmd/seek/main.go`, `cmd/seek/acp_load_test.go` (`TestACPBackend_LoadSession_MissingTranscript`), sibling entry "ACP session/load is an ORDER contract…"
