//go:build !windows && !linux

package bash

func descendantPIDs(rootPID int) []int { return nil }
