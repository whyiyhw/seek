package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// githubAPIBase is the GitHub API root. Overridden by tests via the
// Options.APIBase field; never read directly outside this file.
const githubAPIBase = "https://api.github.com"

// userAgent is what we send for both API + asset downloads. GitHub
// requires a non-empty UA for the API; the asset CDN accepts anything
// but seeing "seek-upgrader/..." in their logs is friendlier than the
// default Go-http-client ID.
const userAgent = "seek-upgrader"

// ghAsset is the subset of the /releases/latest asset payload we use.
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// ghRelease is the subset of the /releases/latest payload we use.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// fetchLatestRelease pulls the newest non-draft, non-prerelease release
// for owner/repo. Returns a helpful error when the repo has no releases
// yet — GitHub returns 404, which would otherwise be opaque.
func fetchLatestRelease(ctx context.Context, client *http.Client, apiBase, owner, repo, token string) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest",
		strings.TrimRight(apiBase, "/"), owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	setGitHubRequestHeaders(req, token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("github: no releases published for %s/%s yet", owner, repo)
	case http.StatusForbidden:
		// Rate-limited or token issue. Surface the reset header if
		// present so the user knows to wait, not retry.
		return nil, fmt.Errorf("github: forbidden (%s); rate-limited or auth required", resp.Status)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("github: decode release: %w", err)
	}
	return &rel, nil
}

// setGitHubRequestHeaders adds the standard headers for GitHub API and
// release-asset downloads. token is optional; when set, Bearer auth
// raises the rate limit from 60/h per IP to 5000/h per token.
func setGitHubRequestHeaders(req *http.Request, token string) {
	req.Header.Set("User-Agent", userAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
