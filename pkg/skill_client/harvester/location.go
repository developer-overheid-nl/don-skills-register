package harvester

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// Location wijst naar een plek in een GitHub-repository: de hele repository
// (https://github.com/owner/repo), of een map of bestand op een ref
// (…/tree/<ref>/<pad> of …/blob/<ref>/<pad>). Een ref met een "/" erin, zoals
// een branch feat/x, wordt niet ondersteund; gebruik dan de commit-SHA.
type Location struct {
	Repo RepoRef
	// Ref is een branch, tag of SHA; leeg betekent de default branch.
	Ref string
	// Path is een map of bestand in de repository; leeg betekent de root.
	Path string
}

// URL geeft de genormaliseerde GitHub-URL van de locatie.
func (l Location) URL() string {
	if l.Ref == "" {
		return l.Repo.URL()
	}
	kind := "tree"
	if path.Base(l.Path) == skillFileName {
		kind = "blob"
	}
	u := l.Repo.URL() + "/" + kind + "/" + l.Ref
	if l.Path != "" {
		u += "/" + l.Path
	}
	return u
}

// ParseGitHubLocation leest een GitHub-URL naar een repository, map of bestand.
func ParseGitHubLocation(raw string) (Location, error) {
	repo, err := ParseGitHubURL(raw)
	if err != nil {
		return Location{}, err
	}
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		return Location{Repo: repo}, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Location{}, fmt.Errorf("ongeldige URL %q: %w", raw, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) <= 2 {
		return Location{Repo: repo}, nil
	}
	if (parts[2] != "tree" && parts[2] != "blob") || len(parts) < 4 || parts[3] == "" {
		return Location{}, fmt.Errorf("verwacht https://github.com/owner/repo, of …/tree/<ref>/<pad> of …/blob/<ref>/<pad>, niet %q", raw)
	}
	p := path.Clean(strings.Join(parts[4:], "/"))
	if p == "." {
		p = ""
	}
	return Location{Repo: repo, Ref: parts[3], Path: p}, nil
}
