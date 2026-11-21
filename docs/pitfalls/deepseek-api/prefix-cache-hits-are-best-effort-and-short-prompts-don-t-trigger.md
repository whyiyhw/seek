### Prefix cache hits are best-effort and short prompts don't trigger
- **Saw**: three back-to-back identical short prompts (110 tokens each) all showed `prompt_cache_hit_tokens=0`
- **Why**: DeepSeek's prefix cache is disk-backed and "best effort". A prefix needs to be at least ~64 tokens (and there's no SLA — the server may evict even longer ones)
- **Fix**: nothing to "fix" code-wise; documented in `docs/prd/archive/v0.md §4.8.1`. Our M2 smoke (system prompt + 5 tools = ~2KB prefix) consistently hits 70%+ across turns; the M0 11-token smoke can't
- **Lesson**: "cache hit ratio" benchmarks should always use realistic-sized prompts; otherwise you'll conclude "the cache doesn't work" when it does
