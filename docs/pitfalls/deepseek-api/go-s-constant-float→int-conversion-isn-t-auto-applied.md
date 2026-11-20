### Go's constant float→int conversion isn't auto-applied
- **Saw**: `cannot convert 65536 * WarnFraction (constant 52428.8) to type int` when writing budget test boundaries
- **Why**: Go's constant folding refuses to silently truncate a constant float result to an int, even when both operands are known at compile time. The test had `int(65536 * WarnFraction)` where WarnFraction was a `const 0.80`
- **Fix**: convert one operand to a runtime value first (`int(float64(65536) * frac)`), or use a tiny helper that does the conversion at runtime. Commit `d038455`
- **Lesson**: when you see "constant X.X of type float64" cannot convert, the answer isn't to add more parentheses — it's to make the expression non-constant
