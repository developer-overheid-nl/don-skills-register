package harvester

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitHubURL(t *testing.T) {
	cases := map[string]RepoRef{
		"https://github.com/org/repo":           {"org", "repo"},
		"https://github.com/org/repo.git":       {"org", "repo"},
		"https://github.com/org/repo/tree/main": {"org", "repo"},
		"org/repo":                              {"org", "repo"},
	}
	for in, want := range cases {
		got, err := ParseGitHubURL(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "https://gitlab.com/org/repo", "https://github.com/org", "org"} {
		_, err := ParseGitHubURL(bad)
		assert.Error(t, err, bad)
	}
}

func TestGitHubClient(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/commits/HEAD", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"sha":"abc"}`))
	})
	mux.HandleFunc("/repos/org/repo/git/trees/abc", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "1", r.URL.Query().Get("recursive"))
		_, _ = w.Write([]byte(`{"truncated":false,"tree":[{"path":"skills","type":"tree"},{"path":"skills/a/SKILL.md","type":"blob"}]}`))
	})
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"), "token niet naar raw host sturen")
		if r.URL.EscapedPath() != "/raw/org/repo/abc/skills/a%20b/SKILL.md" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("inhoud"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &GitHubClient{APIBase: srv.URL, RawBase: srv.URL + "/raw", Token: "secret"}
	repo := RepoRef{"org", "repo"}
	ctx := context.Background()

	sha, err := c.ResolveCommit(ctx, repo, "")
	require.NoError(t, err)
	assert.Equal(t, "abc", sha)

	files, err := c.ListFiles(ctx, repo, sha)
	require.NoError(t, err)
	assert.Equal(t, []string{"skills/a/SKILL.md"}, files)

	body, err := c.ReadFile(ctx, repo, sha, "skills/a b/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, "inhoud", string(body))

	_, err = c.ReadFile(ctx, repo, sha, "bestaat/niet.md")
	assert.ErrorIs(t, err, ErrNotFound)
}
