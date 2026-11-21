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
  type:    the prompt text once the status bar has painted
  answer:  Enter on the plan picker (the highlighted row is "approve")
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
                      [--no-approve] [--deadline 120] -- <seek argv…>
"""
import json
import os
import pty
import select
import signal
import sys
import time

USAGE = ("usage: plan-e2e-drive.py --status FILE --prompt TEXT --done MARKER "
         "[--no-approve] [--deadline SECS] -- <seek argv...>")


def parse(argv):
    opts = {"status": "", "prompt": "", "done": "", "approve": True, "deadline": 120.0}
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--":
            return opts, argv[i + 1:]
        if a in ("--status", "--prompt", "--done", "--deadline"):
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

    buf = b""
    deadline = time.time() + float(opts["deadline"])
    stage = "prompt"
    typed = 0
    text = opts["prompt"]
    exit_sent_at = 0.0
    while time.time() < deadline:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                chunk = b""
            if not chunk:
                break
            buf += chunk
        view = buf.decode("utf-8", "replace")

        # bubbletea paints the status bar only after the model is ready
        # for input; typing earlier loses the keystrokes.
        if stage == "prompt" and "type your prompt" in view:
            time.sleep(0.8)
            for ch in text:
                os.write(fd, ch.encode())
                time.sleep(0.02)
            time.sleep(0.2)
            os.write(fd, b"\r")
            status["prompt_sent"] = True
            typed = 1
            stage = "picker" if opts["approve"] else "done"

        elif stage == "picker" and "Go ahead" in view:
            time.sleep(0.6)
            os.write(fd, b"\r")  # highlighted row = approve (ask per call)
            status["approved"] = True
            stage = "done"

        elif stage == "done" and opts["done"] in view:
            status["done"] = True
            time.sleep(0.6)
            os.write(fd, b"/exit\r")
            exit_sent_at = time.time()
            stage = "exit"

        elif stage == "exit" and time.time() - exit_sent_at > 20:
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

    with open(opts["status"], "w") as f:
        json.dump(status, f)
    sys.stderr.write("plan-e2e-drive: %s\n" % json.dumps(status))
    return 0


if __name__ == "__main__":
    sys.exit(main())
