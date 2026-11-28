### `<br>` inside `<code>` survives display but `textContent` drops it — copy-paste silently corrupts multi-line install commands
- **Saw**: pasting from the macOS/Linux install card's copy button produced one giant line; bash parsed `OS=$(...)$ ARCH=$(...)$ VER=$(...)$ curl ...` as a 3-var env-prefix to `curl`, each value with a trailing literal `$`, fetching `…/v0.2.7$/seek_0.2.7$_darwin$_arm64$.tar.gz` → 404.
- **Why**: the snippet used `<br>` for visual breaks; `Element.textContent` concatenates only text nodes and `<br>` contributes "", so all lines collapsed. The `^(\$|PS>)` strip regex had the `m` flag, but with no real `\n` it only stripped the first line's prompt.
- **Fix**: walk `code.childNodes`, emit `\n` when the node is a `BR`, otherwise its `textContent` — the existing per-line prompt-strip then works. `examples/index.html:1575`
- **Lesson**: a multi-line code block built with `<br>` needs a copy handler reconstructing newlines from the DOM — `textContent` / `innerText` / `outerText` all lose them, differently; better, use `<pre>` + real `\n`.
- **Refs**: `examples/index.html` install section copy-to-clipboard handler
