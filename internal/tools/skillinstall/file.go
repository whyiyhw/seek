package skillinstall

import (
	"io"
	"os"
)

// openFile is a thin os.Open wrapper kept in its own file so the
// stdlib io/os imports don't leak into skillinstall.go's import
// block (which is dominated by domain types).
func openFile(p string) (*os.File, error) { return os.Open(p) }

// readAll is io.ReadAll one level removed for the same reason.
func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }
