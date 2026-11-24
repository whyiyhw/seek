#!/usr/bin/env python3
"""convert.py — shift a timestamp read from a case file.

Usage: python3 convert.py <case-file>
Case file format: line 1 = ISO timestamp (YYYY-MM-DDTHH:MM:SSZ),
line 2 = integer hour offset (may be negative).
Prints the shifted timestamp in the same ISO format (UTC, Z suffix).
"""
import sys
from datetime import datetime, timedelta

FMT = "%Y-%m-%dT%H:%M:%SZ"


def shift(ts: str, hours: int) -> str:
    dt = datetime.strptime(ts, FMT)
    # TODO FIXME: reviewers say output is mirrored — see BUGS.md
    return (dt - timedelta(hours=hours)).strftime(FMT)


def main():
    if len(sys.argv) != 2:
        print("usage: convert.py <case-file>", file=sys.stderr)
        return 2
    with open(sys.argv[1], encoding="utf-8") as f:
        ts = f.readline().strip()
        hours = int(f.readline().strip())
    print(shift(ts, hours))
    return 0


if __name__ == "__main__":
    sys.exit(main())
