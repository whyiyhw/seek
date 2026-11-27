package main

import (
	"testing"

	"github.com/whyiyhw/seek/internal/config"
)

// memoryLaneClient: ACP lanes are excluded even with keys configured —
// bot sessions must stay write-silent (the whole point of the lane).
func TestMemoryLaneClient_ACPExcluded(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers = map[string]config.ProviderConfig{
		"opendesign": {APIKey: "sk-amr-test"},
	}
	if c := memoryLaneClient(cfg, true); c != nil {
		t.Fatalf("acpLane=true must yield nil client, got %v", c)
	}
}

// memoryLaneClient: default resolution inherits the opendesign key and
// endpoint; nil when no key anywhere.
func TestMemoryLaneClient_DefaultResolution(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers = map[string]config.ProviderConfig{
		"opendesign": {APIKey: "sk-amr-test"},
	}
	if c := memoryLaneClient(cfg, false); c == nil {
		t.Fatal("opendesign key configured → client must resolve")
	}

	empty := config.Config{}
	empty.Providers = map[string]config.ProviderConfig{}
	if c := memoryLaneClient(empty, false); c != nil {
		t.Fatalf("no key anywhere → nil client, got %v", c)
	}
}

// memoryLaneClient: a providers.memory pseudo-provider wins over the
// opendesign fallback, so the pipeline can be pinned elsewhere with
// one config block.
func TestMemoryLaneClient_MemoryProviderWins(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers = map[string]config.ProviderConfig{
		"opendesign": {APIKey: "sk-amr-test", BaseURL: "https://amr-link.open-design.ai/v1/chat/completions"},
		"memory":     {APIKey: "sk-custom", BaseURL: "https://mirror.example/v1"},
	}
	if c := memoryLaneClient(cfg, false); c == nil {
		t.Fatal("memory provider configured → client must resolve")
	}
	if got := memoryLaneBaseURL(cfg); got != "https://mirror.example/v1" {
		t.Fatalf("providers.memory.base_url must win, got %q", got)
	}
}

// memoryLaneBaseURL: the compiled default is the opendesign endpoint
// with the compatible-adapter's full-POST suffix trimmed — pkg/deepseek
// appends "/chat/completions" itself.
func TestMemoryLaneBaseURL_DefaultTrimsSuffix(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers = map[string]config.ProviderConfig{}
	if got := memoryLaneBaseURL(cfg); got != "https://amr-link.open-design.ai/v1" {
		t.Fatalf("default base = %q, want gateway v1 root", got)
	}
}

// memoryLaneBaseURL: env wins over everything (proxy/mirror escape
// hatch), and an opendesign base_url override inherits through the
// same trim so one config line moves both lanes.
func TestMemoryLaneBaseURL_EnvAndInherit(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers = map[string]config.ProviderConfig{
		"opendesign": {BaseURL: "https://mirror.example/v1/chat/completions"},
	}
	if got := memoryLaneBaseURL(cfg); got != "https://mirror.example/v1" {
		t.Fatalf("opendesign override must inherit trimmed: %q", got)
	}

	t.Setenv(memoryLaneBaseURLEnv, "https://proxy.example/v1")
	if got := memoryLaneBaseURL(cfg); got != "https://proxy.example/v1" {
		t.Fatalf("env override lost: %q", got)
	}
}

// memoryLaneModel: default is the gateway-served eco id (NOT
// deepseek.ModelV41Flash, so no thinking field on the wire); env wins.
func TestMemoryLaneModel(t *testing.T) {
	if got := memoryLaneModel(); got != memoryLaneDefaultModel {
		t.Fatalf("default model = %q, want %q", got, memoryLaneDefaultModel)
	}
	t.Setenv(memoryLaneModelEnv, " deepseek-v4-pro ")
	if got := memoryLaneModel(); got != "deepseek-v4-pro" {
		t.Fatalf("env model lost / untrimmed: %q", got)
	}
}
