# examples/demo-fullrun.tape — record the FULL autonomous /goal run (promo).
#
#   cd <repo> && vhs examples/demo-fullrun.tape   ->  /tmp/seek-fullrun.mp4
#
# Drives the real TUI: types the doc-audit + pitfall-integration goal, then
# lets seek run autonomously to completion while vhs records the whole thing.
# MP4 (not GIF) + 12fps so a ~20-minute run stays a manageable file; trim/GIF
# later. Sleep is a generous ceiling (run was ~20 min); idle tail is trimmed.

Output "/tmp/seek-fullrun.mp4"

Set Shell         "bash"
Set FontSize      18
Set Width         1680
Set Height        860
Set Padding       24
Set Theme         "Catppuccin Mocha"
Set TypingSpeed   30ms
Set WindowBar     Colorful
Set Framerate     12

Type "seek -yolo"  Enter
Sleep 5s

Type "/goal Audit this codebase for documentation accuracy, module by module. For each package under internal/ and pkg/: read the code and verify the package and exported-symbol doc comments match what the code actually does. Where a doc comment is missing, add an accurate one; where it is wrong or stale, correct it. Then review every chapter under docs/book/ and integrate the relevant entries from docs/pitfalls.md into the corresponding chapter. The goal is met when doc comments across the modules accurately match the code AND the book chapters incorporate the relevant pitfalls. Do not modify code logic — only comments, docs, and book prose."
Sleep 1s
Enter
Sleep 1700s
Sleep 2s
