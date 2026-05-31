package lspclient

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// LSP base protocol framing: each message is a set of `Name: value` headers
// (CRLF-terminated), a blank line, then exactly Content-Length bytes of
// JSON body — HTTP-style. This is the one thing the MCP stdio transport
// (newline-delimited JSON, pkg/mcp) doesn't have, so it lives here.

// writeMessage frames body with a Content-Length header and writes it.
// Callers must serialise writes (header + body must not interleave with
// another message); this package only calls it while holding Client.mu.
func writeMessage(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// readMessage reads one framed message: parse headers until the blank
// line, then read exactly Content-Length bytes. Returns the JSON body.
func readMessage(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line ends the header block
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue // tolerate malformed header lines
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return nil, fmt.Errorf("lspclient: bad Content-Length %q: %w", val, err)
			}
			contentLength = n
		}
		// Other headers (Content-Type) are ignored.
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("lspclient: message missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
