package harvester

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestParseGitHubLocation(t *testing.T) {
	repo := RepoRef{"org", "repo"}
	cases := map[string]Location{
		"https://github.com/org/repo":                             {Repo: repo},
		"https://github.com/org/repo.git/":                        {Repo: repo},
		"org/repo":                                                {Repo: repo},
		"https://github.com/org/repo/tree/main":                   {Repo: repo, Ref: "main"},
		"https://github.com/org/repo/tree/v1/plugins/geo/":        {Repo: repo, Ref: "v1", Path: "plugins/geo"},
		"https://github.com/org/repo/blob/main/skills/a/SKILL.md": {Repo: repo, Ref: "main", Path: "skills/a/SKILL.md"},
	}
	for in, want := range cases {
		got, err := ParseGitHubLocation(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"https://github.com/org/repo/issues/1", "https://github.com/org/repo/tree", "https://gitlab.com/org/repo"} {
		_, err := ParseGitHubLocation(bad)
		assert.Error(t, err, bad)
	}

	assert.Equal(t, "https://github.com/org/repo", Location{Repo: repo}.URL())
	assert.Equal(t, "https://github.com/org/repo/tree/v1/plugins/geo", Location{Repo: repo, Ref: "v1", Path: "plugins/geo"}.URL())
	assert.Equal(t, "https://github.com/org/repo/blob/main/skills/a/SKILL.md", Location{Repo: repo, Ref: "main", Path: "skills/a/SKILL.md"}.URL())
}

func pkt(line string) string { return fmt.Sprintf("%04x%s", len(line)+4, line) }

func tarGz(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: prefix + "/", Typeflag: tar.TypeDir, Mode: 0o755}))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: prefix + "/link", Typeflag: tar.TypeSymlink, Linkname: "README.md"}))
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: prefix + "/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestGitHubClient(t *testing.T) {
	head := strings.Repeat("a", 40)
	feat := strings.Repeat("b", 40)
	tagObj := strings.Repeat("c", 40)
	tagCommit := strings.Repeat("d", 40)
	archives := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/org/repo.git/info/refs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "git-upload-pack", r.URL.Query().Get("service"))
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, pkt("# service=git-upload-pack\n")+"0000"+
			pkt(head+" HEAD\x00multi_ack symref=HEAD:refs/heads/main\n")+
			pkt(head+" refs/heads/main\n")+
			pkt(feat+" refs/heads/feat\n")+
			pkt(tagObj+" refs/tags/v1.0.0\n")+
			pkt(tagCommit+" refs/tags/v1.0.0^{}\n")+
			"0000")
	})
	mux.HandleFunc("/codeload/org/repo/tar.gz/"+head, func(w http.ResponseWriter, r *http.Request) {
		archives++
		_, _ = w.Write(tarGz(t, "repo-"+head, map[string]string{
			"README.md":           "readme",
			"skills/a b/SKILL.md": "inhoud",
			"big.bin":             strings.Repeat("x", maxFileBytes+1),
		}))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &GitHubClient{GitHubBase: srv.URL, CodeloadBase: srv.URL + "/codeload"}
	repo := RepoRef{"org", "repo"}
	ctx := context.Background()

	for ref, want := range map[string]string{"": head, "main": head, "feat": feat, "v1.0.0": tagCommit, feat: feat, "abc1234": "abc1234"} {
		sha, err := c.ResolveCommit(ctx, repo, ref)
		require.NoError(t, err, ref)
		assert.Equal(t, want, sha, ref)
	}
	_, err := c.ResolveCommit(ctx, repo, "bestaat-niet")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = c.ResolveCommit(ctx, RepoRef{"org", "weg"}, "")
	assert.ErrorIs(t, err, ErrNotFound)

	files, err := c.ListFiles(ctx, repo, head)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"README.md", "skills/a b/SKILL.md", "big.bin"}, files, "geen mappen en symlinks")

	body, err := c.ReadFile(ctx, repo, head, "skills/a b/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, "inhoud", string(body))

	_, err = c.ReadFile(ctx, repo, head, "bestaat/niet.md")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = c.ReadFile(ctx, repo, head, "big.bin")
	assert.ErrorContains(t, err, "groter dan")
	assert.Equal(t, 1, archives, "archief één keer downloaden")

	_, err = c.ListFiles(ctx, repo, feat)
	assert.ErrorIs(t, err, ErrNotFound)
}
