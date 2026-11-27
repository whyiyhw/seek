### Explicit `Accept-Encoding: gzip` header disables Go's auto-decompression
- **Saw**: webfetch returned the raw gzip bytes of an HTTPS HTML response instead of decoded text; the model reported "gzip 压缩的 HTML，无法正常解码显示内容".
- **Why**: webfetch.go set `req.Header.Set("Accept-Encoding", "gzip")` manually; Go's `http.Transport` normally requests gzip AND transparently decodes it, but an explicitly set `Accept-Encoding` opts the caller into DIY decompression (`net/http.Transport.DisableCompression` docs).
- **Fix**: remove the manual `Set("Accept-Encoding", ...)`; the transport adds the header and decodes both ends transparently. Commit `4efcdd4`.
- **Lesson**: never set `Accept-Encoding` manually unless you decode by hand — the no-set default gets gzip transparently (br is never auto-handled by stdlib, so adding it means manual decode anyway).
- **Refs**: `internal/tools/webfetch/webfetch.go` (header set site), `internal/tools/webfetch/webfetch_test.go:TestExecute_AutoDecodesGzip`
