### A blanket "POST needs Origin" CSRF gate breaks the script face — the W1.2 write gate is credential-conditional
- **Saw**: §5.2 read literally ("写面二选一：同源 Origin 或 X-Seek-Token") would 403 every curl POST `/msg?token=` — no Origin header outside browsers, and `TestWeb_MsgRequiresToken` (query token, no Origin → 202) is a pinned zero-regression contract.
- **Why**: CSRF is a COOKIE-born threat; a request bearing the token as query/Bearer already proves possession of the secret — its own CSRF defense. Only cookie-authenticated requests (browser-attached silently) need the extra bar.
- **Fix**: `authKind` reports WHICH credential passed; `writeGate` (Origin ∈ {self origin} or constant-time X-Seek-Token) runs only on the `credCookie` path; card `c_w5lidm`.
- **Lesson**: CSRF gates key off the credential FORM, not the HTTP method — "which requests can a third party make the browser send" is the real question; script-face credentials are exempt by construction.
- **Refs**: `internal/controlplane/webfrontend_auth.go` `writeGate`; `docs/prd/feature-web-frontend.md` §5.2.
