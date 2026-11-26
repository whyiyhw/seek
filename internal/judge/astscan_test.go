package judge

// astscan_test.go — the AST half of the zero-state acceptance check.
//
// It deliberately lives in a _test.go file: the SCANNER itself needs
// os/parser access (reading and parsing the package's own sources),
// while the PRODUCTION files must not. That asymmetry is the point —
// TestNoStateImports asserts production files never import a write
// surface, and nothing here weakens it.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type scannedFile struct {
	path    string
	imports []string
	src     string
}

// parsePackageFiles returns every .go file in this directory (test files
// included; callers filter) with its import list and raw source.
func parsePackageFiles() ([]scannedFile, error) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	out := make([]scannedFile, 0, len(names))
	for _, name := range names {
		path, err := filepath.Abs(name)
		if err != nil {
			return nil, err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		sf := scannedFile{path: name, src: string(src)}
		for _, spec := range f.Imports {
			if spec.Path == nil {
				continue
			}
			sf.imports = append(sf.imports, strings.Trim(spec.Path.Value, `"`))
		}
		out = append(out, sf)
	}
	return out, nil
}
