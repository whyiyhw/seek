# Known bug

`convert.py <case>` prints a timestamp shifted the WRONG WAY: `plus3.txt`
expects `+3h` but the output is 3 hours EARLIER, not later. Cross-midnight
and month-boundary cases must stay correct after the fix (the bug is a
direction inversion, not a formatting issue — the format is already right).
