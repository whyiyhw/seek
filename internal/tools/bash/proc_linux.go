//go:build linux

package bash

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// descendantPIDs returns every descendant of rootPID by walking
// /proc/[pid]/children (Linux 3.5+). Used when a child escapes the
// parent's process group — e.g. sudo/setsid create a new session and
// SIGKILL to -PGID only kills the shell wrapper.
func descendantPIDs(rootPID int) []int {
	seen := map[int]bool{rootPID: true}
	var out []int
	queue := []int{rootPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range procChildren(pid) {
			if seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

func procChildren(pid int) []int {
	if out := procChildrenFile(pid); len(out) > 0 {
		return out
	}
	// /proc/PID/children is missing on some containers/kernels, or not
	// yet populated right after fork — scan /proc/*/stat for PPID match.
	return procChildrenScan(pid)
}

func procChildrenFile(pid int) []int {
	path := fmt.Sprintf("/proc/%d/children", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(data))
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}

func procChildrenScan(parentPID int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		ppid, ok := procPPID(pid)
		if ok && ppid == parentPID {
			out = append(out, pid)
		}
	}
	return out
}

func procPPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// stat: pid (comm) state ppid ... — comm may contain ')'.
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(strings.TrimSpace(s[i+1:]))
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
