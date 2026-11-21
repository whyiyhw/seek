#!/usr/bin/env python3
"""plan-e2e-drive.py — drive a real `seek` TUI through one plan-mode turn
inside a pseudo-terminal (eval v2 E3, card c_c5wwey).

WHY A PTY

`propose` asks its approval question through `askuser.Policy`, whose
callback is wired only on the TUI path (cmd/seek/main.go wires
SetAskFn after the -p/-json branch returns; print mode therefore always
answers ErrNoCallback — docs/pitfalls.md "propose in print mode always
errors…"). The plan toolchain's approval gate is *interactive by
design*, so the only honest way to exercise it end to end unattended is
to give seek a terminal and answer the picker.

WHAT IT DOES

  spawn:   seek <argv…> on a pty, 80x24, TERM=xterm-256color
  type:    the prompt text once the input placeholder has painted
  answer:  Down + Enter on the plan picker (selects the SECOND row,
           "Go ahead — auto-approve per step": approval arms the
           plan tool's per-step pre-approval gate, so writes inside a
           plan(start)-armed step auto-pass and the driver never has
           to answer per-call y/N prompts mid-chain)
  wait:    a done marker in the terminal stream (the scripted model's
           final text), then "/exit" to persist + quit
  report:  a JSON status file — approved / done / exited / timeout — so
           the caller can turn terminal facts into assertions instead of
           grepping ANSI soup.

Exit code is 0 whenever the drive itself ran to completion (including a
timeout — a timeout is a *result*, reported in the status file, not a
driver crash). Missing python pty support / usage errors exit 2.

Usage:
    plan-e2e-drive.py --status S.json --prompt "text" --done DONE-MARK \
                      [--record RAW.bin] [--no-approve] [--deadline 120] \
                      -- <seek argv…>
    --no-approve: the lane expects NO plan picker (e.g. the resume
                  lane, which only probes plan state) — skip straight
                  from prompt to done-wait instead of stalling on
                  "Go ahead" forever.
"""
import fcntl
import json
import os
import pty
import re
import select
import signal
import struct
import sys
import termios
import time

USAGE = ("usage: plan-e2e-drive.py --status FILE --prompt TEXT --done MARKER "
         "[--record FILE] [--no-approve] [--deadline SECS] -- <seek argv...>")

# CSI/OSC escape sequences. The inline renderer repaints per delta, so
# a marker word can arrive SPLIT across repaints ("RESUME-\x1b[1mE2E")
# — substring probes must run against the ANSI-stripped view or they
# miss real completions.
_ANSI = re.compile(r"\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b[()][B0]|\x1b[=>][0-9;]*[a-zA-Z]")


def clean(view):
    return _ANSI.sub("", view)


def parse(argv):
    opts = {"status": "", "prompt": "", "done": "", "record": "",
            "approve": True, "deadline": 120.0}
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--":
            return opts, argv[i + 1:]
        if a in ("--status", "--prompt", "--done", "--record", "--deadline"):
            if i + 1 >= len(argv):
                sys.stderr.write(USAGE + "\n")
                sys.exit(2)
            v = argv[i + 1]
            key = a[2:]
            opts[key] = float(v) if key == "deadline" else v
            i += 2
            continue
        if a == "--no-approve":
            opts["approve"] = False
            i += 1
            continue
        sys.stderr.write(USAGE + "\n")
        sys.exit(2)
    return opts, []


def main():
    opts, cmd = parse(sys.argv[1:])
    if not opts["status"] or not opts["prompt"] or not opts["done"] or not cmd:
        sys.stderr.write(USAGE + "\n")
        sys.exit(2)

    status = {"approved": False, "done": False, "exited": False,
              "timed_out": False, "prompt_sent": False}
    try:
        pid, fd = pty.fork()
    except OSError as e:  # pragma: no cover - platform guard
        sys.stderr.write("plan-e2e-drive: pty unavailable: %s\n" % e)
        sys.exit(2)

    if pid == 0:  # child
        os.environ.setdefault("TERM", "xterm-256color")
        try:
            os.execvp(cmd[0], cmd)
        except OSError:
            os._exit(127)

    # bubbletea renders against the reported winsize; a 0x0 pty makes
    # the first frames degenerate. 80x24 is the conventional floor.
    try:
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
    except OSError:
        pass

    buf = b""
    deadline = time.time() + float(opts["deadline"])
    stage = "prompt"
    text = opts["prompt"]
    exit_sent_at = 0.0
    term_sent_at = 0.0
    while time.time() < deadline:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                chunk = b""
            if not chunk:
                # EOF on the pty master: the child closed its
                # controlling terminal — for a TUI that IS the exit
                # (the farewell summary prints right before). Reap so
                # `exited` reports the fact instead of staying false
                # and tripping the kill path.
                try:
                    os.waitpid(pid, 0)
                except ChildProcessError:
                    pass
                status["exited"] = True
                break
            buf += chunk
        # Strip escape sequences once per poll: substring probes
        # (placeholder / picker / done marker) run on the clean view.
        view = clean(buf.decode("utf-8", "replace"))

        # bubbletea paints the input placeholder only after the model is
        # ready for input; typing earlier loses the keystrokes. The
        # placeholder text depends on startup flags and turn count
        # (placeholder.go): --yolo shows "YOLO active", turn 0 shows
        # "Ask seek anything", cache/off-peak states show "type your
        # prompt", resumed sessions rotate tips ("type @", "type /").
        # ANY hit means "input is live".
        if stage == "prompt" and any(
                s in view for s in ("YOLO active", "type your prompt",
                                    "Ask seek anything", "type @", "type /")):
            time.sleep(0.8)
            for ch in text:
                os.write(fd, ch.encode())
                time.sleep(0.02)
            time.sleep(0.2)
            os.write(fd, b"\r")
            status["prompt_sent"] = True
            # Lanes without a picker (resume probe) must not wait for
            # "Go ahead" — that stage would never clear.
            stage = "picker" if opts["approve"] else "done"

        elif stage == "picker" and "Go ahead" in view:
            time.sleep(0.6)
            # Row 0 is "approve (ask per call)" — under it every write
            # prompts y/N and the driver would have to keep answering.
            # Down selects row 1, "approve — auto-approve per step":
            # plan(start) then arms the pre-approval gate for writes.
            os.write(fd, b"\x1b[B")
            time.sleep(0.3)
            os.write(fd, b"\r")
            status["approved"] = True
            stage = "done"

        elif stage == "done" and opts["done"] in view:
            status["done"] = True
            time.sleep(0.6)
            os.write(fd, b"/exit\r")
            exit_sent_at = time.time()
            stage = "exit"

        elif stage == "exit":
            # /exit should be prompt; when it isn't (a hung save, a
            # modal eating the keystroke), escalate TERM then KILL so
            # the driver always terminates inside its own deadline.
            # Session data is auto-saved per turn, so even the KILL
            # path leaves a readable transcript for judging.
            now = time.time()
            if now - exit_sent_at > 10 and not term_sent_at:
                try:
                    os.kill(pid, signal.SIGTERM)
                except OSError:
                    pass
                term_sent_at = now
            elif term_sent_at and now - term_sent_at > 8:
                break

        if stage == "exit":
            try:
                if os.waitpid(pid, os.WNOHANG)[0] == pid:
                    status["exited"] = True
                    break
            except ChildProcessError:
                status["exited"] = True
                break

    if not status["exited"]:
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
        try:
            os.waitpid(pid, 0)
        except ChildProcessError:
            pass
        status["timed_out"] = not status["done"]

    if opts["record"]:
        with open(opts["record"], "wb") as f:
            f.write(buf)

    with open(opts["status"], "w") as f:
        json.dump(status, f)
    sys.stderr.write("plan-e2e-drive: %s\n" % json.dumps(status))
    return 0


if __name__ == "__main__":
    sys.exit(main())
