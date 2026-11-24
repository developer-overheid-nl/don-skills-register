package harvester

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultGitHubBase   = "https://github.com"
	defaultCodeloadBase = "https://codeload.github.com"
	maxFileBytes        = 1 << 20   // 1 MiB; een SKILL.md hoort ruim kleiner te zijn
	maxRefsBytes        = 16 << 20  // lijst met branches en tags
	maxArchiveBytes     = 100 << 20 // gedownload archief (gzip)
	maxKeptBytes        = 64 << 20  // bewaarde bestandsinhoud per archief
	archiveCacheSize    = 4
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

// GitHubClient leest publieke repositories zonder GitHub API en zonder token,
// zodat de API rate limit niet geldt:
//
//   - een ref wordt een commit via het git-protocol
//     (github.com/<owner>/<repo>.git/info/refs, net als git ls-remote);
//   - bestanden komen uit het archief van die commit
//     (codeload.github.com/<owner>/<repo>/tar.gz/<sha>), één download per
//     repository per harvest.
type GitHubClient struct {
	GitHubBase   string
	CodeloadBase string
	HTTP         *http.Client

	mu       sync.Mutex
	archives map[string]*repoArchive
	order    []string
}

// repoArchive is de inhoud van een repository op één commit.
type repoArchive struct {
	paths []string
	files map[string][]byte
	// tooLarge bevat bestanden die wel bestaan maar groter zijn dan maxFileBytes.
	tooLarge map[string]bool
}

func NewGitHubClientFromEnv() *GitHubClient {
	return &GitHubClient{
		GitHubBase:   envOr("GITHUB_BASE", defaultGitHubBase),
		CodeloadBase: envOr("GITHUB_CODELOAD_BASE", defaultCodeloadBase),
		HTTP:         &http.Client{Timeout: 2 * time.Minute},
	}
}

// ResolveCommit geeft de commit-SHA voor een ref (branch, tag of SHA). Een lege
// ref betekent de default branch.
func (c *GitHubClient) ResolveCommit(ctx context.Context, repo RepoRef, ref string) (string, error) {
	if looksLikeSHA(ref) {
		return ref, nil
	}
	u := fmt.Sprintf("%s/%s/%s.git/info/refs?service=git-upload-pack", base(c.GitHubBase, defaultGitHubBase), url.PathEscape(repo.Owner), url.PathEscape(repo.Repo))
	body, err := c.get(ctx, u, maxRefsBytes)
	if err != nil {
		return "", fmt.Errorf("commit %s@%s: %w", repo, refOrHead(ref), err)
	}
	refs, err := parseRefs(body)
	if err != nil {
		return "", fmt.Errorf("commit %s@%s: %w", repo, refOrHead(ref), err)
	}
	candidates := []string{"HEAD"}
	if ref != "" {
		candidates = []string{"refs/heads/" + ref, "refs/tags/" + ref + "^{}", "refs/tags/" + ref, ref}
	}
	for _, name := range candidates {
		if sha, ok := refs[name]; ok {
			return sha, nil
		}
	}
	if isShortSHA(ref) {
		// codeload accepteert ook een verkorte SHA.
		return ref, nil
	}
	return "", fmt.Errorf("commit %s@%s: %w", repo, refOrHead(ref), ErrNotFound)
}

// ListFiles geeft alle bestandspaden in de repository op een commit.
func (c *GitHubClient) ListFiles(ctx context.Context, repo RepoRef, sha string) ([]string, error) {
	a, err := c.archive(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), a.paths...), nil
}

// ReadFile leest een bestand op een commit.
func (c *GitHubClient) ReadFile(ctx context.Context, repo RepoRef, sha, path string) ([]byte, error) {
	a, err := c.archive(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	if a.tooLarge[path] {
		return nil, fmt.Errorf("bestand %s@%s:%s: groter dan %d bytes", repo, sha, path, maxFileBytes)
	}
	content, ok := a.files[path]
	if !ok {
		return nil, fmt.Errorf("bestand %s@%s:%s: %w", repo, sha, path, ErrNotFound)
	}
	return content, nil
}

// archive haalt het archief van een commit op, of geeft het uit de cache.
func (c *GitHubClient) archive(ctx context.Context, repo RepoRef, sha string) (*repoArchive, error) {
	key := repo.String() + "@" + sha
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.archives[key]; ok {
		return a, nil
	}
	u := fmt.Sprintf("%s/%s/%s/tar.gz/%s", base(c.CodeloadBase, defaultCodeloadBase), url.PathEscape(repo.Owner), url.PathEscape(repo.Repo), url.PathEscape(sha))
	body, err := c.get(ctx, u, maxArchiveBytes)
	if err != nil {
		return nil, fmt.Errorf("archief %s@%s: %w", repo, sha, err)
	}
	a, err := readArchive(body)
	if err != nil {
		return nil, fmt.Errorf("archief %s@%s: %w", repo, sha, err)
	}
	if c.archives == nil {
		c.archives = map[string]*repoArchive{}
	}
	if len(c.order) >= archiveCacheSize {
		delete(c.archives, c.order[0])
		c.order = c.order[1:]
	}
	c.archives[key] = a
	c.order = append(c.order, key)
	return a, nil
}

// readArchive leest een tar.gz van codeload. Alle paden beginnen met één map
// (<repo>-<sha>/), die wordt weggelaten.
func readArchive(body []byte) (*repoArchive, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a := &repoArchive{files: map[string][]byte{}, tooLarge: map[string]bool{}}
	kept := 0
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		_, p, ok := strings.Cut(hdr.Name, "/")
		if !ok || p == "" {
			continue
		}
		a.paths = append(a.paths, p)
		if hdr.Size > maxFileBytes {
			a.tooLarge[p] = true
			continue
		}
		if kept += int(hdr.Size); kept > maxKeptBytes {
			return nil, fmt.Errorf("repository is groter dan %d bytes", maxKeptBytes)
		}
		content, err := io.ReadAll(io.LimitReader(tr, maxFileBytes))
		if err != nil {
			return nil, err
		}
		a.files[p] = content
	}
	return a, nil
}

// parseRefs leest de advertentie van git-upload-pack (pkt-lines) naar een
// map van ref naar SHA. De eerste ref bevat na een NUL de capabilities.
func parseRefs(body []byte) (map[string]string, error) {
	refs := map[string]string{}
	r := bufio.NewReader(bytes.NewReader(body))
	for {
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("ongeldig antwoord van git: %w", err)
		}
		n, err := strconv.ParseUint(string(size[:]), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("ongeldig antwoord van git: %w", err)
		}
		if n == 0 {
			continue // flush-pkt
		}
		if n < 4 {
			return nil, errors.New("ongeldig antwoord van git")
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(r, line); err != nil {
			return nil, fmt.Errorf("ongeldig antwoord van git: %w", err)
		}
		text := strings.TrimSuffix(string(line), "\n")
		if strings.HasPrefix(text, "#") {
			continue
		}
		text, _, _ = strings.Cut(text, "\x00")
		sha, name, ok := strings.Cut(text, " ")
		if ok && looksLikeSHA(sha) {
			refs[name] = sha
		}
	}
	if len(refs) == 0 {
		return nil, errors.New("geen refs gevonden")
	}
	return refs, nil
}

func (c *GitHubClient) get(ctx context.Context, u string, limit int64) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "don-skills-register")
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
	// GitHub geeft 401 voor een repository die niet bestaat of privé is.
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrNotFound
	case resp.StatusCode >= http.StatusMultipleChoices:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("antwoord groter dan %d bytes", limit)
	}
	return body, nil
}

func refOrHead(ref string) string {
	if ref == "" {
		return "HEAD"
	}
	return ref
}

func isShortSHA(ref string) bool {
	if len(ref) < 7 || len(ref) >= 40 {
		return false
	}
	for _, r := range ref {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func base(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return strings.TrimRight(v, "/")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
