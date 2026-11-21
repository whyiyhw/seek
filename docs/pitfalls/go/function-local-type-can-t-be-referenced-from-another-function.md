### Function-local type can't be referenced from another function
- **Saw**: attempted to call `buildGroupedIndexString([]entryInfo{...})` where `entryInfo` was defined inside `buildMIndex()`. Go compiler rejected: "undefined: entryInfo"
- **Why**: Go type declarations inside function bodies are scoped to that function only. Unlike closures (which capture values, not types), the type itself is invisible outside its declaring function, even from another function in the same package
- **Fix**: moved `entryInfo` to package-level type. The struct is used by two functions (`buildMIndex` and `buildGroupedIndexString`), so it cannot be local to either
- **Lesson**: Go's scoping rules for types follow the same lexical block rules as variables — a type declared inside a function is as private as a variable there. If two functions share a type, it must live at package or file level
- **Refs**: `internal/memory/hook.go:entryInfo`
