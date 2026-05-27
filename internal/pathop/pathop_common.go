package pathop

import (
	"os"
	"path/filepath"
	"strings"
)

func pathContainsDir(envPath, dir string, fold bool) bool {
	dir = filepath.Clean(dir)
	for _, p := range strings.Split(envPath, string(os.PathListSeparator)) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		cleaned := filepath.Clean(p)
		if fold {
			if strings.EqualFold(cleaned, dir) {
				return true
			}
		} else if cleaned == dir {
			return true
		}
	}
	return false
}
