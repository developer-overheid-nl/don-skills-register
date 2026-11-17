package harvester

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultGitHubAPIBase = "https://api.github.com"
	defaultGitHubRawBase = "https://raw.githubusercontent.com"
	maxFileBytes         = 1 << 20 // 1 MiB; een SKILL.md hoort ruim kleiner te zijn
)

// ErrNotFound wordt teruggegeven als een bestand of repository niet bestaat.
var ErrNotFound = errors.New("niet gevonden")

// RepoRef verwijst naar een GitHub-repository.
type RepoRef struct {
	Owner string
	Repo  string
}

func (r RepoRef) String() string { return r.Owner + "/" + r.Repo }

// URL geeft de publieke URL van de repository.
func (r RepoRef) URL() string { return "https://github.com/" + r.String() }

// ParseGitHubURL haalt owner/repo uit een GitHub-URL of een "owner/repo" string.
func ParseGitHubURL(raw string) (RepoRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RepoRef{}, fmt.Errorf("lege repository")
	}
	path := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return RepoRef{}, fmt.Errorf("ongeldige URL %q: %w", raw, err)
		}
		if !strings.EqualFold(u.Host, "github.com") && !strings.EqualFold(u.Host, "www.github.com") {
			return RepoRef{}, fmt.Errorf("alleen github.com wordt ondersteund, niet %q", u.Host)
		}
		path = u.Path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return RepoRef{}, fmt.Errorf("verwacht owner/repo in %q", raw)
	}
	return RepoRef{Owner: parts[0], Repo: strings.TrimSuffix(parts[1], ".git")}, nil
}

// GitHubClient leest repositories via de GitHub API (commits en trees) en
// raw.githubusercontent.com (bestandsinhoud). De raw host telt niet mee voor
// de API rate limit; zet GITHUB_TOKEN voor een ruimere limiet op de API.
type GitHubClient struct {
	APIBase string
	RawBase string
	Token   string
	HTTP    *http.Client
}

func NewGitHubClientFromEnv() *GitHubClient {
	return &GitHubClient{
		APIBase: envOr("GITHUB_API_BASE", defaultGitHubAPIBase),
		RawBase: envOr("GITHUB_RAW_BASE", defaultGitHubRawBase),
		Token:   strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ResolveCommit geeft de commit-SHA voor een ref (branch, tag of SHA). Een lege
// ref betekent de default branch.
func (c *GitHubClient) ResolveCommit(ctx context.Context, repo RepoRef, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	var out struct {
		SHA string `json:"sha"`
	}
	u := fmt.Sprintf("%s/repos/%s/%s/commits/%s", c.apiBase(), repo.Owner, repo.Repo, url.PathEscape(ref))
	if err := c.getJSON(ctx, u, &out); err != nil {
		return "", fmt.Errorf("commit %s@%s: %w", repo, ref, err)
	}
	if out.SHA == "" {
		return "", fmt.Errorf("commit %s@%s: lege sha", repo, ref)
	}
	return out.SHA, nil
}

// ListFiles geeft alle bestandspaden (blobs) in de repository op een commit.
func (c *GitHubClient) ListFiles(ctx context.Context, repo RepoRef, sha string) ([]string, error) {
	var out struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	u := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", c.apiBase(), repo.Owner, repo.Repo, url.PathEscape(sha))
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil, fmt.Errorf("tree %s@%s: %w", repo, sha, err)
	}
	if out.Truncated {
		return nil, fmt.Errorf("tree %s@%s is afgekapt door GitHub; repository is te groot", repo, sha)
	}
	paths := make([]string, 0, len(out.Tree))
	for _, entry := range out.Tree {
		if entry.Type == "blob" {
			paths = append(paths, entry.Path)
		}
	}
	return paths, nil
}

// ReadFile leest een bestand op een commit.
func (c *GitHubClient) ReadFile(ctx context.Context, repo RepoRef, sha, path string) ([]byte, error) {
	u := fmt.Sprintf("%s/%s/%s/%s/%s", strings.TrimRight(c.rawBase(), "/"), repo.Owner, repo.Repo, sha, escapePath(path))
	body, err := c.get(ctx, u, false)
	if err != nil {
		return nil, fmt.Errorf("bestand %s@%s:%s: %w", repo, sha, path, err)
	}
	return body, nil
}

func (c *GitHubClient) getJSON(ctx context.Context, u string, out any) error {
	body, err := c.get(ctx, u, true)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *GitHubClient) get(ctx context.Context, u string, api bool) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "don-skills-register")
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode >= http.StatusMultipleChoices:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFileBytes {
		return nil, fmt.Errorf("bestand groter dan %d bytes", maxFileBytes)
	}
	return body, nil
}

func (c *GitHubClient) apiBase() string {
	if c.APIBase == "" {
		return defaultGitHubAPIBase
	}
	return strings.TrimRight(c.APIBase, "/")
}

func (c *GitHubClient) rawBase() string {
	if c.RawBase == "" {
		return defaultGitHubRawBase
	}
	return c.RawBase
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
