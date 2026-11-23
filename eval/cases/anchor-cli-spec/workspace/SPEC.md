# filter_entries.py — specification

Write a Python 3 CLI `filter_entries.py` (stdlib only) that reads
`entries.json` from the CURRENT directory and prints matching entry ids.

CLI arguments:

- `--min-score FLOAT` (default `0`): keep entries with `score >= min-score`
- `--tag NAME` (optional): when given, keep only entries whose `tags`
  array contains NAME

Input shape (`entries.json`): a JSON array of objects with fields
`id` (string), `score` (number), `tags` (array of strings).

Output contract:

- one id per line, sorted ascending as STRINGS (lexicographic)
- nothing else on stdout (no headers, no separators)
- exit 0 on success; exit 2 with a message on stderr if entries.json is
  missing or invalid JSON
