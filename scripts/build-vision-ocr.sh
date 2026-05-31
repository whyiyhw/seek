#!/usr/bin/env bash
# Build the macOS Vision OCR helper (v7 柱 Q) into a target directory.
# No-op on non-macOS so it's safe to call unconditionally from install /
# release flows. Bash 3.2 compatible (stock macOS).
set -eu

out_dir="${1:-.}"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "vision_ocr: skipping — not macOS (other platforms set ocr.command instead)"
  exit 0
fi

if ! command -v swiftc >/dev/null 2>&1; then
  echo "vision_ocr: swiftc not found — install the Xcode Command Line Tools (xcode-select --install)" >&2
  exit 1
fi

src="$(cd "$(dirname "$0")/.." && pwd)/tools/ocr/vision_ocr.swift"
swiftc -O "$src" -o "$out_dir/vision_ocr"
echo "vision_ocr: built → $out_dir/vision_ocr (~$(wc -c < "$out_dir/vision_ocr" | tr -d ' ') bytes)"
