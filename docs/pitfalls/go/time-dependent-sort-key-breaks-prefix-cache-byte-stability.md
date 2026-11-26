### Time-dependent sort key breaks prefix-cache byte-stability
- **Saw**: M-index truncation path sorted entries by `Score(e, now, halfLife)` where `now` changed on every prompt — two adjacent prompts with identical disk state could produce different M-index byte sequences, causing a full prefix-cache miss.
- **Why**: `Score()` uses `exp(-(now - lastActive) / halfLife)`; even with no metadata change, time passing between prompts shifts scores enough to push a borderline entry into or out of the top-K budget window, changing the byte sequence.
- **Fix**: replaced the time-dependent Score sort with a TIME-INDEPENDENT key `pinned desc → recall_count desc → name asc`, changing byte output only when entry metadata changes (add/remove/recall/GC); documented the trade in `buildMIndex`'s byte-stability doc.
- **Lesson**: any sort or filter key used in a prompt-injection path must be time-independent unless you'll pay the cache-miss cost — for M-index at 1500 token budget the cache hit is worth more than the marginal ranking improvement.
- **Refs**: `internal/memory/hook.go:buildMIndex`
