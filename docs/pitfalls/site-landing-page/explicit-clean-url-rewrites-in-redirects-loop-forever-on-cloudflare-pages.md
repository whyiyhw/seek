### Explicit clean-URL rewrites in `_redirects` loop forever on Cloudflare Pages
- **Saw**: on the deployed Cloudflare Pages docs site exactly 7 pages (`commands`, `architecture`, `sessions`, `memory`, `skills`, `subagent`, `cron`) hung with `ERR_TOO_MANY_REDIRECTS`; `curl -I` showed an infinite 307 loop to themselves while every other page returned 200.
- **Why**: `examples/_redirects` had `"/docs/commands  /docs/commands.html  200"` rewrites for those 7; Cloudflare Pages already serves clean URLs and canonicalises `*.html` back to extensionless, so the rewrite loops — the broken set was exactly the `_redirects` entries.
- **Fix**: delete the 200 clean-URL rewrites (redundant on Cloudflare Pages); kept only `/docs → /docs/ 301` plus a NOTE comment. `examples/_redirects`
- **Lesson**: don't hand-write clean-URL rewrites for hosts that do them natively (Cloudflare Pages, Vercel `cleanUrls`); when a subset of similar pages breaks, diff it against config. Deploy behaviour is invisible from `file://` and the repo — verify with `curl -I`.
- **Refs**: `examples/_redirects`; symptom surfaced via `curl -sSL -o /dev/null -w '%{http_code}'` against the live site
