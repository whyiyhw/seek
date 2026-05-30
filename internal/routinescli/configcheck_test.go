package routinescli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/config"
)

func writeTestConfig(t *testing.T, cfg config.Config) {
	t.Helper()
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigCheck_NoWebhooks(t *testing.T) {
	withTestHome(t)
	var out bytes.Buffer
	if err := Run([]string{"config", "check"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("no-webhooks check should succeed, got %v", err)
	}
	if !strings.Contains(out.String(), "no push webhooks configured") {
		t.Errorf("unexpected output: %s", out.String())
	}
}

func TestConfigCheck_ValidAndInvalid(t *testing.T) {
	withTestHome(t)
	// Offline check (no --probe): scheme + format only, no network.
	writeTestConfig(t, config.Config{PushWebhooks: []config.PushWebhook{
		{URL: "https://ntfy.sh/topic", Format: "ntfy"}, // ok
		{URL: "file:///bad", Format: "raw"},            // bad scheme
		{URL: "https://ok", Format: "telegram"},        // bad format
	}})
	var out bytes.Buffer
	err := Run([]string{"config", "check"}, &out, &bytes.Buffer{})
	if err == nil {
		t.Fatal("config with invalid webhooks should return an error")
	}
	s := out.String()
	if !strings.Contains(s, "✓") || !strings.Contains(s, "✗") {
		t.Errorf("output should mark valid ✓ and invalid ✗:\n%s", s)
	}
	if !strings.Contains(err.Error(), "2 of 3") {
		t.Errorf("error should count failures, got: %v", err)
	}
}

func TestConfigCheck_AllValid(t *testing.T) {
	withTestHome(t)
	writeTestConfig(t, config.Config{PushWebhooks: []config.PushWebhook{
		{URL: "https://ntfy.sh/topic"},                      // empty format → raw
		{URL: "http://192.168.1.5/hook", Format: "discord"}, // private LAN allowed
	}})
	var out bytes.Buffer
	if err := Run([]string{"config", "check"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("all-valid check should succeed, got %v", err)
	}
	if !strings.Contains(out.String(), "2 webhook(s) OK") {
		t.Errorf("unexpected output: %s", out.String())
	}
}

func TestConfig_UnknownSub(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"config", "bogus"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("unknown config subcommand should error naming it, got %v", err)
	}
}
