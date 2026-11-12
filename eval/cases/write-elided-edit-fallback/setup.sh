#!/usr/bin/env bash
# Generate the fixture into a per-case scratch dir (eval/run.sh setup
# hook). Deterministic; NOT committed as testdata — 74 KiB of padding
# does not belong in git, and the generator doubles as documentation
# of the shape the case needs.
#
# Shape contract (verified below):
#   - 100 lines, each ~740 bytes -> ~74 KiB total
#   - above the 32 KiB whole-read threshold, within the 200-line
#     default read limit -> one read reaches EOF, output > 64 KiB cap
#     -> middle elided (head 8 KiB + tail 24 KiB kept)
#   - line 55 (~40 KiB in) carries the only `retention_days: 30` -> it
#     sits inside the elided middle, invisible to a full read
set -e
dir=eval/tmp/write-elided-edit-fallback
mkdir -p "$dir"
f="$dir/config-dump.txt"
: > "$f"
i=1
while [ "$i" -le 100 ]; do
  if [ "$i" -eq 55 ]; then
    printf 'entry_%03d | section=retention | retention_days: 30 | scope=global | owner=svc-ops | note=this slot owns the retention knob, see runbook RB-114 | flags=0x2F | audit=every 24h | -ullamco-laboris-nisi-ut-aliquip-commodo-consequat-duis-aute-irure-dolor-in-reprehenderit-in-voluptate-padding=lorem-ipsum-dolor-sit-amet-consectetur-adipiscing-elit-sed-do-eiusmod-tempor-incididunt-ut-labore-et-dolore-magna-aliqua-ut-enim-ad-minim-veniam-quis-nostrud\n' "$i" >> "$f"
  else
    printf 'entry_%03d | section=s%02d | weight=%03d | flags=0x%02X | owner=svc-%02d | checksum=a1b2c3d4e5f6%03d | quota=%04d | region=ap-| pad2=lorem-ipsum-dolor-sit-amet-consectetur-adipiscing-elit-sed-do-eiusmod-tempor-incididunt-ut-labore-et-dolore-magna-aliqua-ut-enim-ad-minim-veniam-quis-nostrud-exercitation-tempor-ipsum-dolor-sit-amet-consectetur-adipiscing-elit-sed-do-eiusmod-tempor-incididunt-ut-labore-et-dolore-magna-aliqua-ut-enim-ad-minim-veniam-quis-nostrud-exercitation| -ullamco-laboris-nisi-ut-aliquip-commodo-consequat-duis-aute-irure-dolor-in-reprehenderit-in-voluptate-padding=lorem-ipsum-dolor-sit-amet-consectetur-adipiscing-elit-sed-do-eiusmod-tempor-incididunt-ut-labore-et-dolore-magna-aliqua-ut-enim-ad-minim-veniam-quis-nostrud-exercitation\n' \
      "$i" $((i % 17)) $((i * 7 % 101)) $((i % 64)) $((i % 23)) "$i" $((i * 13 % 997)) >> "$f"
  fi
  i=$((i + 1))
done

# Sanity-check the shape contract; fail loudly if it drifts.
bytes=$(wc -c < "$f" | tr -d ' ')
hits=$(grep -c 'retention_days: 30' "$f" | tr -d ' ')
if [ "$bytes" -lt 70000 ] || [ "$bytes" -gt 78000 ] || [ "$hits" -ne 1 ]; then
  echo "write-elided-edit-fallback setup: fixture shape drifted (bytes=$bytes hits=$hits)" >&2
  exit 1
fi
