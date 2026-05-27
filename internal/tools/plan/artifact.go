// artifact.go — write a markdown snapshot of an approved plan to
// ~/.seek/projects/<id>/plans/<YYYYMMDD>-<HHMM>-<slug>.md.
//
// The artifact is a **write-once** document — a contract between the
// user and the model captured at the moment of approval. It does NOT
// participate in runtime state reconstruction (that's transcript
// event-sourcing's job, see reconstruct.go) and never gets updated.
// Step progress, adjustments, cancellation — none of these touch the
// file. The artifact's value is purely for human consumption: open
// it in vim, browse the plans directory, reuse it on another project.
//
// Design constraints (PRD docs/prd/feature-plan-mode.md §八):
//
//   - **Failure is observational, not fatal.** The caller passes the
//     return error up to the propose tool result, which surfaces it
//     to both model and user; the rest of the plan workflow continues.
//     A full disk MUST NOT brick plan mode.
//
//   - **Atomic write.** os.WriteFile to a sibling .tmp file, then
//     os.Rename to the final path. Partial writes (e.g. SIGKILL
//     mid-flush) leave only a .tmp behind, never a corrupt .md.
//
//   - **No external deps.** strings + regexp for slug extraction; no
//     NLP, no stemming. The slug is a hint, not a contract.

package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// ArtifactMetadata is the input to WriteArtifact. Every field that
// shows up in the file's front-matter is here so the call site is
// the single source of truth — there's no implicit "pull from
// ambient state" inside the writer.
type ArtifactMetadata struct {
	// Problem is the propose tool's problem statement verbatim.
	Problem string
	// Steps is the propose tool's steps verbatim.
	Steps []string
	// WhyNow is the optional rationale; empty string omits the
	// "## Why now" section.
	WhyNow string
	// SessionID identifies the session in which approval happened.
	// Empty string is tolerated (e.g. --no-save mode) — the
	// "Session" front-matter line is then omitted.
	SessionID string
	// ProjectAbsPath is the project root (CWD at startup). Required;
	// drives both the destination directory and the front-matter
	// "Project" field.
	ProjectAbsPath string
	// Batch is true when the user approved via "auto-approve per
	// step"; false for the per-call default. Recorded so future you
	// can answer "which mode did I approve this plan in?".
	Batch bool
	// ApprovedAt is the approval timestamp. The writer formats it
	// into the filename (YYYYMMDD-HHMM) and the "Approved" front-
	// matter line. Pass time.Now() unless you're in a test.
	ApprovedAt time.Time
}

// maxConflictSuffix bounds the conflict-resolution loop so a hostile
// filesystem (every name exists) can't spin forever. 99 distinct
// plans approved in the same minute on the same project is far
// beyond plausible.
const maxConflictSuffix = 99

// WriteArtifact writes a markdown snapshot of meta to
// ~/.seek/projects/<id>/plans/<YYYYMMDD>-<HHMM>-<slug>.md and returns
// the resulting absolute path. Failures are returned for the caller
// to log + surface; the artifact is observational, so callers should
// not treat an error as fatal to the plan workflow.
//
// The write is atomic: a .tmp sibling is written first, then renamed
// into place. A partial write therefore leaves at most a .tmp turd,
// never a corrupt final file.
func WriteArtifact(meta ArtifactMetadata) (string, error) {
	if strings.TrimSpace(meta.ProjectAbsPath) == "" {
		return "", fmt.Errorf("plan artifact: ProjectAbsPath is required")
	}
	if strings.TrimSpace(meta.Problem) == "" {
		return "", fmt.Errorf("plan artifact: Problem is required")
	}
	if len(meta.Steps) == 0 {
		return "", fmt.Errorf("plan artifact: Steps is required")
	}
	if meta.ApprovedAt.IsZero() {
		return "", fmt.Errorf("plan artifact: ApprovedAt is required")
	}

	dir, err := paths.ProjectPlans(meta.ProjectAbsPath)
	if err != nil {
		return "", fmt.Errorf("plan artifact: resolve dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("plan artifact: mkdir %q: %w", dir, err)
	}

	slug := extractSlug(meta.Problem)
	base := meta.ApprovedAt.Format("20060102-1504") + "-" + slug

	path, err := resolveNonConflictingPath(dir, base)
	if err != nil {
		return "", err
	}

	body := renderArtifact(meta, slug)

	// Atomic write: write to .tmp sibling, rename to final. Rename
	// is atomic on the same filesystem (POSIX), so a reader either
	// sees the old file (or nothing) or the complete new file —
	// never a half-written one.
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("plan artifact: write tmp %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("plan artifact: rename %q → %q: %w", tmpPath, path, err)
	}
	return path, nil
}

// resolveNonConflictingPath returns dir/<base>.md if that doesn't
// exist; otherwise appends -2, -3, … up to maxConflictSuffix.
// Errors when the filesystem reports too many distinct same-minute
// plans (essentially never in practice).
func resolveNonConflictingPath(dir, base string) (string, error) {
	candidate := filepath.Join(dir, base+".md")
	if !fileExists(candidate) {
		return candidate, nil
	}
	for n := 2; n <= maxConflictSuffix; n++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d.md", base, n))
		if !fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("plan artifact: %d same-minute conflicts on %q — refusing to keep counting", maxConflictSuffix, base)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// renderArtifact composes the markdown body. The format is locked
// per PRD §8.5 / §8.12 — adding fields is fine, renaming or removing
// existing ones is a breaking change.
func renderArtifact(meta ArtifactMetadata, slug string) string {
	mode := "per-call"
	if meta.Batch {
		mode = "auto-approve-per-step"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", humanizeSlug(slug))
	fmt.Fprintf(&sb, "- **Approved**: %s\n", meta.ApprovedAt.Format("2006-01-02 15:04:05 -0700"))
	if meta.SessionID != "" {
		fmt.Fprintf(&sb, "- **Session**: %s\n", meta.SessionID)
	}
	fmt.Fprintf(&sb, "- **Approval mode**: %s\n", mode)
	fmt.Fprintf(&sb, "- **Project**: %s\n", meta.ProjectAbsPath)

	sb.WriteString("\n## Problem\n\n")
	sb.WriteString(strings.TrimSpace(meta.Problem))
	sb.WriteString("\n\n## Steps\n\n")
	for i, s := range meta.Steps {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(s))
	}

	if strings.TrimSpace(meta.WhyNow) != "" {
		sb.WriteString("\n## Why now\n\n")
		sb.WriteString(strings.TrimSpace(meta.WhyNow))
		sb.WriteString("\n")
	}

	sb.WriteString("\n---\n\n")
	sb.WriteString("*Write-once snapshot of the plan as approved. Step progress lives in the session transcript (`~/.seek/sessions/<session>.jsonl`), not here. To browse all plans for this project: `ls ~/.seek/projects/<id>/plans/`.*\n")

	return sb.String()
}

// Stopwords removed from slug candidates. Intentionally small — the
// goal isn't NLP-grade keyword extraction, just trimming the most
// obvious filler so "Refactor the auth middleware to use a per-request
// token store" doesn't produce a slug like "refactor-the-auth".
var slugStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"but": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "do": true, "does": true, "did": true, "will": true,
	"would": true, "could": true, "should": true, "may": true, "might": true,
	"must": true, "can": true, "of": true, "in": true, "on": true,
	"at": true, "to": true, "for": true, "with": true, "by": true,
	"from": true, "as": true, "into": true, "that": true, "this": true,
	"these": true, "those": true, "we": true, "our": true, "us": true,
	"you": true, "your": true, "it": true, "its": true,
	"they": true, "them": true, "their": true,
	"so": true, "if": true, "than": true, "then": true, "such": true,
}

// slugAlphanumRE matches runs of non-alphanumeric ASCII; used both to
// tokenise the problem and to strip remaining junk from each token.
// Unicode letters intentionally collapse to space — slugs are
// filename-safe ASCII only (we don't trust every shell + fs to
// preserve multibyte filenames identically across copy / sync / zip).
var slugAlphanumRE = regexp.MustCompile(`[^a-z0-9]+`)

const (
	maxSlugWords = 5
	minWordLen   = 2
	fallbackSlug = "plan"
)

// extractSlug picks up to maxSlugWords keywords from problem, dropping
// stopwords and very short tokens, and joins them kebab-case. The
// result is filename-safe ASCII. Returns fallbackSlug when nothing
// usable comes out (empty problem, all-stopwords, all-non-ascii).
func extractSlug(problem string) string {
	tokens := slugAlphanumRE.Split(strings.ToLower(problem), -1)
	var keywords []string
	for _, t := range tokens {
		if t == "" || len(t) < minWordLen || slugStopwords[t] {
			continue
		}
		keywords = append(keywords, t)
		if len(keywords) >= maxSlugWords {
			break
		}
	}
	if len(keywords) == 0 {
		return fallbackSlug
	}
	return strings.Join(keywords, "-")
}

// humanizeSlug turns "auth-middleware-refactor" into
// "Auth Middleware Refactor" for the H1 line.
func humanizeSlug(slug string) string {
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if len(p) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
