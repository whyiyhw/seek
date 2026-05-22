package upgrade

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"
)

// parseChecksums parses GoReleaser's checksums.txt (sha256sum -c
// compatible: "<hex>  <filename>" per line). Returns map[name] = hex
// sha256. Lines beginning with '#' and blank lines are ignored. The
// filename is taken as-is (no path canonicalisation) — GoReleaser's
// output uses basenames only.
func parseChecksums(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "<hex>  <name>" — sha256sum writes two spaces by default,
		// but be liberal: any whitespace run after the hex works.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		// Some checksum tools prefix '*' to denote binary mode; strip.
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if len(sum) != 64 {
			continue // not sha256, skip
		}
		out[name] = sum
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("checksums: scan: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("checksums: file contained no valid sha256 lines")
	}
	return out, nil
}

// hashingReader wraps r and accumulates a sha256 of every byte that
// flows through. Used during download so we can verify the checksum
// without a second read pass over the (possibly 20MB) file.
type hashingReader struct {
	r io.Reader
	h hash.Hash
}

func newHashingReader(r io.Reader) *hashingReader {
	return &hashingReader{r: r, h: sha256.New()}
}

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	if n > 0 {
		_, _ = h.h.Write(p[:n])
	}
	return n, err
}

func (h *hashingReader) Sum() string {
	return hex.EncodeToString(h.h.Sum(nil))
}
