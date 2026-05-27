package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/pathop"
)

// willUseTUI reports whether run() will enter the interactive TUI rather
// than print/json/rpc/benchmark short-circuits.
func willUseTUI(jsonOut bool, prompt string, benchmarkTask string, rpcMode bool, dreamFlag bool) bool {
	if jsonOut || prompt != "" || stdinIsPiped() || benchmarkTask != "" || rpcMode || dreamFlag {
		return false
	}
	return true
}

// maybeWindowsPATHPrompt runs once per install on Windows TUI launches.
// It writes HKCU\Environment\Path without WM_SETTINGCHANGE broadcast.
func maybeWindowsPATHPrompt() error {
	if runtime.GOOS != "windows" || stdinIsPiped() {
		return nil
	}

	cfg, err := config.Load()
	if err != nil || cfg.PathPromptDone {
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	binDir := filepath.Dir(exe)

	inUser, err := pathop.IsInUserPATH(binDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read user PATH: %v\n", err)
		return nil
	}
	if inUser || pathop.IsInPATH(binDir) {
		cfg.PathPromptDone = true
		return config.Save(cfg)
	}

	fmt.Fprint(os.Stderr, "seek is not in your PATH. Add it so you can run seek from any terminal? [Y/n]: ")
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		return nil
	}
	ans := strings.TrimSpace(answer)
	if ans == "" || strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
		added, err := pathop.EnsureInPATH(binDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not add to PATH: %v\n", err)
		} else if added {
			fmt.Fprintln(os.Stderr, "Added seek to PATH. Restart your terminal for the change to take effect.")
		} else {
			fmt.Fprintln(os.Stderr, "seek is already in your user PATH. Restart your terminal for the change to take effect.")
		}
	}

	cfg.PathPromptDone = true
	return config.Save(cfg)
}

func runInstall() error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("install: adding seek to PATH is only supported on Windows")
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("install: locate self: %w", err)
	}
	dir := filepath.Dir(exe)

	inUser, err := pathop.IsInUserPATH(dir)
	if err != nil {
		return fmt.Errorf("install: read user PATH: %w", err)
	}
	if inUser {
		if pathop.IsInPATH(dir) {
			fmt.Println("seek is already in PATH")
		} else {
			fmt.Println("seek is already in your user PATH. Restart your terminal for the change to take effect.")
		}
		return nil
	}
	if pathop.IsInPATH(dir) {
		fmt.Println("seek is already in PATH")
		return nil
	}

	added, err := pathop.EnsureInPATHWithBroadcast(dir)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if added {
		fmt.Println("seek added to PATH. Restart your terminal for the change to take effect.")
	} else {
		fmt.Println("seek is already in your user PATH. Restart your terminal for the change to take effect.")
	}
	return nil
}
