//go:build windows

package pathop

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// IsInPATH reports whether dir appears in the current process PATH
// (case-insensitive on Windows).
func IsInPATH(dir string) bool {
	dir, err := cleanAbsDir(dir)
	if err != nil {
		return false
	}
	return pathContainsDir(os.Getenv("PATH"), dir, true)
}

// IsInUserPATH reports whether dir is already listed in the user's
// permanent PATH (HKCU\Environment\Path), expanding %VAR% references.
func IsInUserPATH(dir string) (bool, error) {
	dir, err := cleanAbsDir(dir)
	if err != nil {
		return false, err
	}
	currentPath, err := readUserPath()
	if err != nil {
		return false, err
	}
	return userPathContains(currentPath, dir), nil
}

// EnsureInPATH adds dir to the user's permanent PATH via
// HKCU\Environment\Path and updates the current process environment
// immediately. No broadcast — caller should inform the user to
// restart their terminal. Prefer this for startup nudges.
// Returns true when a new registry entry was appended.
func EnsureInPATH(dir string) (bool, error) {
	return ensureInPATH(dir, false)
}

// EnsureInPATHWithBroadcast does the same as EnsureInPATH but also
// broadcasts WM_SETTINGCHANGE so Explorer and new terminal windows
// immediately pick up the change. Use this for explicit install
// commands (seek -install), not for startup nudges.
func EnsureInPATHWithBroadcast(dir string) (bool, error) {
	return ensureInPATH(dir, true)
}

func ensureInPATH(dir string, withBroadcast bool) (bool, error) {
	absDir, err := cleanAbsDir(dir)
	if err != nil {
		return false, err
	}

	k, err := registry.OpenKey(registry.CURRENT_USER,
		"Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()

	currentPath, valType, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return false, err
	}
	if err == registry.ErrNotExist {
		currentPath = ""
		valType = registry.EXPAND_SZ
	}

	if userPathContains(currentPath, absDir) {
		ensureProcessPATH(absDir)
		return false, nil
	}

	var newPath string
	switch {
	case currentPath == "":
		newPath = absDir
	case strings.HasSuffix(currentPath, ";"):
		newPath = currentPath + absDir
	default:
		newPath = currentPath + ";" + absDir
	}

	if valType == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", newPath)
	} else {
		err = k.SetStringValue("Path", newPath)
	}
	if err != nil {
		return false, err
	}

	ensureProcessPATH(absDir)

	if withBroadcast {
		broadcastEnvironmentChange()
	}
	return true, nil
}

func readUserPath() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		"Environment", registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()

	currentPath, _, err := k.GetStringValue("Path")
	if err == registry.ErrNotExist {
		return "", nil
	}
	return currentPath, err
}

func userPathContains(currentPath, absDir string) bool {
	for _, p := range strings.Split(currentPath, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		expanded := filepath.Clean(os.ExpandEnv(p))
		if strings.EqualFold(expanded, absDir) {
			return true
		}
	}
	return false
}

func cleanAbsDir(dir string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absDir), nil
}

func ensureProcessPATH(absDir string) {
	if pathContainsDir(os.Getenv("PATH"), absDir, true) {
		return
	}
	procPath := os.Getenv("PATH")
	if procPath != "" {
		_ = os.Setenv("PATH", procPath+";"+absDir)
	} else {
		_ = os.Setenv("PATH", absDir)
	}
}

func broadcastEnvironmentChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")

	const (
		hwndBroadcast   = 0xFFFF
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
		timeoutMs       = 2000
	)

	envStr := windows.StringToUTF16Ptr("Environment")
	_, _, _ = sendMessageTimeout.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(envStr)),
		smtoAbortIfHung,
		timeoutMs,
		0,
	)
}
