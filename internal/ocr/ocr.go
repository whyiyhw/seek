// Package ocr turns image references in user input into recognized text
// via a LOCAL OCR engine (v7 柱 Q). seek's model is text-only; this lets
// "fix this @err.png" work offline by OCR-expanding the image to text
// before the prompt reaches the provider — no VLM, no network.
//
// The package owns ONLY detection + the OCR exec + the injection format.
// Where it hooks (the user-input call sites, NOT Agent.Prompt — that
// would wrongly OCR subagent prompts too) and where its config comes from
// are the caller's job. The OCR engine is pluggable: an explicit command
// (ocr.command / SEEK_OCR_CMD) or the bundled macOS Vision helper.
package ocr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultTimeout = 15 * time.Second

// errNoEngine signals that no OCR command/helper is configured — the
// caller degrades gracefully (filename placeholder + one-time hint).
var errNoEngine = errors.New("no OCR engine configured")

// Options configures OCR. Command (if set) wins; otherwise Helper (the
// bundled macOS Vision binary) is used; if neither, OCR is unavailable.
type Options struct {
	Command   []string                              // explicit engine; image path appended as the last arg
	Helper    string                                // path to a prebuilt vision_ocr helper (fallback)
	Provision func(context.Context) (string, error) // lazy: produce a helper path on first image (compile-on-demand); fallback after Helper
	Languages string                                // hint passed via SEEK_OCR_LANGUAGES (helper may honor)
	Timeout   time.Duration                         // per-image; 0 → 15s
}

// Available reports whether any OCR engine is configured. A Provision
// closure counts as available — it may still fail at first use (e.g. no
// swiftc), which surfaces as an in-band hint rather than a missing engine.
func (o Options) Available() bool {
	return len(o.Command) > 0 || o.Helper != "" || o.Provision != nil
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true,
	".tiff": true, ".bmp": true, ".heic": true, ".gif": true,
}

// Expand scans text for tokens that reference an EXISTING image file,
// OCRs each, and appends one injection block per image. The original
// text is preserved (blocks are appended). Returns text unchanged when
// there are no image references. Never errors — failures become
// in-band `[image: … — OCR 失败/未启用]` notes so the conversation
// continues (the model is told it's a non-text or unavailable image).
func Expand(ctx context.Context, text string, opt Options) string {
	paths := DetectImageRefs(text)
	if len(paths) == 0 {
		return text
	}
	blocks := make([]string, 0, len(paths))
	for _, p := range paths {
		blocks = append(blocks, block(ctx, p, opt))
	}
	return text + "\n\n" + strings.Join(blocks, "\n")
}

// DetectImageRefs returns, in order and de-duplicated, the tokens in text
// that reference an existing regular file with an image extension.
//
// The stat-gate is the load-bearing correctness guard (PRD §路径假阳性):
// a bare ".png" substring inside a code snippet, URL, or error message
// is NOT a file and must not trigger OCR. A leading "@" is stripped so
// "@shot.png" works.
func DetectImageRefs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Fields(text) {
		p := cleanToken(tok)
		if p == "" || seen[p] {
			continue
		}
		if !imageExts[strings.ToLower(filepath.Ext(p))] {
			continue
		}
		if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
			continue // not a real file → not an image reference
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// cleanToken peels @ prefixes, surrounding quotes/backticks/brackets, and
// trailing sentence punctuation — looping until stable so nested wrappers
// like "(@shot.png)," resolve to "shot.png" regardless of order.
func cleanToken(tok string) string {
	for {
		prev := tok
		tok = strings.Trim(tok, "\"'`()[]<>")
		tok = strings.TrimRight(tok, ".,;:!?")
		tok = strings.TrimPrefix(tok, "@")
		if tok == prev {
			return tok
		}
	}
}

func block(ctx context.Context, path string, opt Options) string {
	out, err := Run(ctx, path, opt)
	return formatBlock(filepath.Base(path), out, err)
}

// formatBlock renders an OCR result (or failure) as the in-band injection
// block. Shared by the file-path path (block) and the in-memory path
// (ExpandImageData, M-P.5) so both produce byte-identical markup. Never
// errors — failures become an in-band note so the conversation continues.
func formatBlock(name, out string, err error) string {
	if err != nil {
		if errors.Is(err, errNoEngine) {
			return fmt.Sprintf("[image: %s — OCR 未启用：macOS 需先构建 vision 助手；其他平台请设置 ocr.command]", name)
		}
		return fmt.Sprintf("[image: %s — OCR 失败: %v]", name, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Sprintf("[image: %s — OCR 未识别到文字，可能是图形/无文本截图]", name)
	}
	return fmt.Sprintf("[image: %s — OCR]\n%s\n[/image: %s]", name, strings.TrimRight(out, "\n"), name)
}

// Run executes OCR on one image and returns the recognized text.
// Resolution order: Options.Command, then the bundled Helper. Returns
// errNoEngine when neither is set, and a wrapped error on exec/timeout.
func Run(ctx context.Context, path string, opt Options) (string, error) {
	var argv []string
	switch {
	case len(opt.Command) > 0:
		argv = opt.Command
	case opt.Helper != "":
		argv = []string{opt.Helper}
	case opt.Provision != nil:
		// Lazy compile-on-demand (e.g. embedded macOS Vision helper).
		// Uses the parent ctx, not the per-image timeout — a one-time
		// build can legitimately outlast a single OCR call.
		h, err := opt.Provision(ctx)
		if err != nil {
			return "", err
		}
		argv = []string{h}
	default:
		return "", errNoEngine
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Image path is appended as the LAST argument (PRD / task brief).
	args := append(append([]string{}, argv[1:]...), path)
	cmd := exec.CommandContext(cctx, argv[0], args...)
	if opt.Languages != "" {
		cmd.Env = append(os.Environ(), "SEEK_OCR_LANGUAGES="+opt.Languages)
	}
	out, err := cmd.Output()
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timed out after %s", timeout)
		}
		return "", err
	}
	return string(out), nil
}
