### `json.Unmarshal` silently drops unknown fields — LLM typos produce useless errors
- **Saw**: model called `list_dir({"directory": "/path", "depth": 1})` (wrong field). Error: `list_dir: path is required` — no mention of `directory` being unknown, no hint at valid fields. Model retried identical args and hit the same wall
- **Why**: Go's `json.Unmarshal` drops unknown fields silently: `directory` ignored, `path` zero-valued, and the nil check produced the generic "required" error with zero diagnostic value
- **Fix**: `internal/tools/tool.go` adds `UnmarshalStrict` (`json.Decoder.DisallowUnknownFields`) and `MissingField` helpers. Error now reads: `list_dir: bad arguments: json: unknown field "directory". Got: {"directory":...}. Valid fields: path, depth, show_hidden`. All eight tool `Execute` methods updated. Regression test: `TestListDir_UnknownFieldSurfacesActionableError`. Commit (this one)
- **Lesson**: any `json.Unmarshal` target at an LLM tool boundary must use `DisallowUnknownFields`: silent drops make self-correction impossible — the model has nothing to act on
- **Refs**: `internal/tools/tool.go:UnmarshalStrict`; `internal/tools/listdir/listdir_test.go`
