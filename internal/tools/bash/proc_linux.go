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
