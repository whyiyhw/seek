### LLM thinking-mode artifact tokens leaked into generated source code
- **Saw**: a comment line ended with `// changes there. response` — "response" was a fragment of DeepSeek's thinking-mode token `｜end▁of▁thinking｜>` leaked into the file — invisible in editors (rendered as whitespace), present as raw UTF-8.
- **Why**: DeepSeek V4-Flash with thinking enabled occasionally emits internal thinking-mode delimiters inside tool calls or generated content; in a file write (via the `write` tool) they land as literal bytes that look normal but are semantically wrong.
- **Fix**: `grep -r 'end▁of▁thinking'` finds and removes leaked tokens; the writing tool should post-strip known artifact patterns; CI `grep -rn 'end▁of▁thinking' --include='*.go'` catches future leaks.
- **Lesson**: an agent writing code with thinking-mode models needs a post-write filter for known delimiter tokens — rare (1 in ~hundreds of tool calls), invisible to human review, silent to the compiler (they look like comments); CI grep is the cheapest insurance.
- **Refs**: `internal/memorycli/memorycli.go:358` (before fix)
