package lspclient

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestFraming_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"x"}`)
	if err := writeMessage(&buf, body); err != nil {
		t.Fatal(err)
	}
	// The wire form must be a Content-Length header + blank line + body.
	if !strings.HasPrefix(buf.String(), "Content-Length: 37\r\n\r\n") {
		t.Fatalf("bad frame header: %q", buf.String())
	}
	got, err := readMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round-trip = %q, want %q", got, body)
	}
}

func TestFraming_ToleratesExtraHeaders(t *testing.T) {
	raw := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
		"Content-Length: 2\r\n\r\n{}"
	got, err := readMessage(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{}" {
		t.Fatalf("body = %q, want {}", got)
	}
}

func TestFraming_MissingContentLength(t *testing.T) {
	raw := "Content-Type: x\r\n\r\n{}"
	if _, err := readMessage(bufio.NewReader(strings.NewReader(raw))); err == nil {
		t.Fatal("expected error when Content-Length is missing")
	}
}
