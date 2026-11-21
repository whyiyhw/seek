### Asserting on SSE reply payloads with angle brackets fails — Go's json.Marshal HTML-escapes by default
- **Saw**: an e2e substring like `用法：/bind <项目路径>` never matches the SSE frame, which reads `用法：/bind \u003c项目路径\u003e` on the wire.
- **Why**: `json.Marshal` (and the `json.NewEncoder` default) HTML-escape `<`, `>`, `&` to `\u003c` etc.; every `Send`/`SendStream` envelope rides that marshal.
- **Fix**: assert bracket-free substrings (e.g. `用法：/bind` + `目录必须存在`); commit with card `c_evbzyh`.
- **Lesson**: substring assertions against marshaled JSON must avoid HTML-sensitive characters — pick neighbouring plain text, don't chase the escape spelling.
