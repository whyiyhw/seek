// Package fimcomplete implements the DeepSeek-specific `fim_complete`
// tool. It exposes DeepSeek's fill-in-the-middle endpoint to the agent
// loop so the model can request cheap, gap-filling completions when it
// already has confident surroundings (PRD §4.8.3).
//
// The tool is intentionally read-only — it returns the completion text;
// the agent then decides whether to apply it via `edit`. Keeping the apply
// step in `edit` means the same review/permission story applies and we
// don't duplicate a write path here.
//
// This tool is only useful with DeepSeek as the provider. When seek grows
// the second-tier providers (M6), cmd/seek will hide this tool whenever
// the active provider isn't *deepseek.Client.
package fimcomplete

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":          {"type": "string", "description": "File to read context from."},
    "before_marker": {"type": "string", "description": "Exact substring that appears immediately before the gap. Must be unique in the file."},
    "after_marker":  {"type": "string", "description": "Optional. Substring that appears immediately after the gap. If omitted the gap extends to EOF."},
    "max_tokens":    {"type": "integer", "description": "Hard cap on completion length. Default 256, max 2048.", "minimum": 1, "maximum": 2048}
  },
  "required": ["path", "before_marker"],
  "additionalProperties": false
}`)

const description = "Generate a fill-in-the-middle completion at a position in a file using DeepSeek's FIM endpoint (cheaper than chat for in-place edits). Returns the completion text WITHOUT applying it — call `edit` afterwards if you want to insert it. DeepSeek-only."

type Args struct {
	Path         string `json:"path"`
	BeforeMarker string `json:"before_marker"`
	AfterMarker  string `json:"after_marker,omitempty"`
	MaxTokens    int    `json:"max_tokens,omitempty"`
}

type Tool struct {
	client *deepseek.Client
	model  string
}

// New returns a fim_complete tool bound to the given DeepSeek client. Pass
// "" for model to default to deepseek-chat.
func New(c *deepseek.Client, model string) Tool {
	if model == "" {
		model = deepseek.ModelChat
	}
	return Tool{client: c, model: model}
}

func (Tool) Name() string            { return "fim_complete" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

const (
	defaultMaxTokens = 256
	maxAllowedTokens = 2048
)

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("fim_complete", raw, &a, "path", "before_marker", "after_marker", "max_tokens"); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", tools.MissingField("fim_complete", "path", raw, "path", "before_marker", "after_marker", "max_tokens")
	}
	if a.BeforeMarker == "" {
		return "", tools.MissingField("fim_complete", "before_marker", raw, "path", "before_marker", "after_marker", "max_tokens")
	}
	if a.MaxTokens <= 0 {
		a.MaxTokens = defaultMaxTokens
	}
	if a.MaxTokens > maxAllowedTokens {
		a.MaxTokens = maxAllowedTokens
	}

	clean := filepath.Clean(a.Path)
	raw2, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("fim_complete: %w", err)
	}
	content := string(raw2)

	if c := strings.Count(content, a.BeforeMarker); c != 1 {
		return "", fmt.Errorf("fim_complete: before_marker must occur exactly once in %s (found %d)", clean, c)
	}
	beforeEnd := strings.Index(content, a.BeforeMarker) + len(a.BeforeMarker)
	prompt := content[:beforeEnd]

	var suffix string
	if a.AfterMarker != "" {
		afterStart := strings.Index(content[beforeEnd:], a.AfterMarker)
		if afterStart < 0 {
			return "", fmt.Errorf("fim_complete: after_marker not found after before_marker in %s", clean)
		}
		// Ensure after_marker is unique in the post-before region too —
		// duplicates make the intent ambiguous.
		if strings.Count(content[beforeEnd:], a.AfterMarker) != 1 {
			return "", fmt.Errorf("fim_complete: after_marker must be unique after before_marker (found %d occurrences)",
				strings.Count(content[beforeEnd:], a.AfterMarker))
		}
		suffix = content[beforeEnd+afterStart:]
	}

	resp, err := t.client.FIM(ctx, &deepseek.FIMRequest{
		Model:     t.model,
		Prompt:    prompt,
		Suffix:    suffix,
		MaxTokens: a.MaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("fim_complete: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("fim_complete: empty response")
	}
	completion := resp.Choices[0].Text

	header := fmt.Sprintf("=== FIM completion for %s ===\n", clean)
	header += fmt.Sprintf("(usage: prompt %d, completion %d, cache hit %d / miss %d)\n\n",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Usage.PromptCacheHitTokens, resp.Usage.PromptCacheMissTokens)
	footer := "\n=== call `edit` with old_string=before_marker+after_marker and new_string=before_marker+<completion>+after_marker to apply ==="
	return header + completion + footer, nil
}
