package harvester

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	marketSHA = "1111111111111111111111111111111111111111"
	extSHA    = "2222222222222222222222222222222222222222"
)

// fakeRepos simuleert GitHub met per repository één commit en een set bestanden.
type fakeRepos struct {
	commits map[string]string            // repo -> sha
	files   map[string]map[string]string // repo -> path -> inhoud
}

func (f *fakeRepos) ResolveCommit(_ context.Context, repo RepoRef, _ string) (string, error) {
	sha, ok := f.commits[repo.String()]
	if !ok {
		return "", ErrNotFound
	}
	return sha, nil
}

func (f *fakeRepos) ListFiles(_ context.Context, repo RepoRef, _ string) ([]string, error) {
	var out []string
	for p := range f.files[repo.String()] {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeRepos) ReadFile(_ context.Context, repo RepoRef, _ string, path string) ([]byte, error) {
	content, ok := f.files[repo.String()][path]
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(content), nil
}

func skillMD(name string) string {
	return "---\nname: " + name + "\ndescription: Beschrijving van " + name + "\n---\nBody\n"
}

func newFakeMarketplace(t *testing.T) *fakeRepos {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"name":     "overheid-plugins",
		"metadata": map[string]any{"description": "Plugins voor de overheid"},
		"plugins": []any{
			map[string]any{"name": "lokaal", "source": "./plugins/lokaal", "version": "1.0.0", "author": map[string]any{"name": "Geonovum"}, "tags": []string{"geo"}},
			map[string]any{"name": "extern", "source": map[string]any{"source": "github", "repo": "org/extern"}, "author": "Team X", "keywords": []string{"api"}},
			map[string]any{"name": "npm-plugin", "source": map[string]any{"source": "npm", "package": "@org/x"}},
		},
	})
	require.NoError(t, err)
	return &fakeRepos{
		commits: map[string]string{"org/market": marketSHA, "org/extern": extSHA},
		files: map[string]map[string]string{
			"org/market": {
				".claude-plugin/marketplace.json":      string(manifest),
				"plugins/lokaal/skills/a/SKILL.md":     skillMD("a"),
				"plugins/lokaal/skills/kapot/SKILL.md": "geen frontmatter",
				"plugins/ander/skills/b/SKILL.md":      skillMD("b"),
			},
			"org/extern": {
				"skills/c/SKILL.md":              skillMD("c"),
				"node_modules/x/skills/SKILL.md": skillMD("node"),
				"README.md":                      "readme",
			},
		},
	}
}

func TestHarvestMarketplace(t *testing.T) {
	h := New(newFakeMarketplace(t))
	source := models.Source{Id: "src-1", Type: models.SourceTypeMarketplace, Url: "https://github.com/org/market"}

	res, err := h.Harvest(context.Background(), source)
	require.NoError(t, err)

	assert.Equal(t, "overheid-plugins", res.Name)
	assert.Equal(t, "Plugins voor de overheid", res.Description)

	require.Len(t, res.Plugins, 2, "npm-plugin wordt overgeslagen")
	assert.Equal(t, "lokaal", res.Plugins[0].Name)
	assert.Equal(t, "Geonovum", res.Plugins[0].Author)
	assert.Equal(t, "https://github.com/org/market", res.Plugins[0].RepoUrl)
	assert.Equal(t, "extern", res.Plugins[1].Name)
	assert.Equal(t, "Team X", res.Plugins[1].Author)
	assert.Equal(t, []string{"api"}, res.Plugins[1].Tags)

	names := map[string]models.Skill{}
	for _, s := range res.Skills {
		names[s.Name] = s
	}
	assert.Len(t, res.Skills, 2)
	require.Contains(t, names, "a", "skill onder ./plugins/lokaal")
	require.Contains(t, names, "c", "skill uit externe repo")
	assert.NotContains(t, names, "b", "skill buiten de plugin-map hoort er niet bij")
	assert.NotContains(t, names, "node")

	assert.Equal(t, res.Plugins[0].Id, *names["a"].PluginID)
	assert.Equal(t, marketSHA, names["a"].Ref)
	assert.Equal(t, extSHA, names["c"].Ref)
	assert.Equal(t, "skills/c/SKILL.md", names["c"].Path)
	assert.NotEmpty(t, names["a"].Hash)

	joined := strings.Join(res.Warnings, "\n")
	assert.Contains(t, joined, "npm")
	assert.Contains(t, joined, "kapot")
}

func TestHarvestIDsAreStable(t *testing.T) {
	h := New(newFakeMarketplace(t))
	source := models.Source{Id: "src-1", Type: models.SourceTypeMarketplace, Url: "https://github.com/org/market"}
	first, err := h.Harvest(context.Background(), source)
	require.NoError(t, err)
	second, err := h.Harvest(context.Background(), source)
	require.NoError(t, err)
	assert.Equal(t, first.Skills[0].Id, second.Skills[0].Id)
	assert.Equal(t, first.Plugins[0].Id, second.Plugins[0].Id)
}

func TestHarvestPlugin(t *testing.T) {
	repos := newFakeMarketplace(t)
	repos.files["org/market"]["plugins/lokaal/.claude-plugin/plugin.json"] = `{"name":"lokaal-plugin","description":"Lokale skills","version":"2.0.0","author":{"name":"Geonovum"},"keywords":["geo"]}`
	h := New(repos)

	res, err := h.Harvest(context.Background(), models.Source{Id: "src-p", Type: models.SourceTypePlugin, Url: "https://github.com/org/market/tree/main/plugins/lokaal"})
	require.NoError(t, err)
	require.Len(t, res.Plugins, 1)
	p := res.Plugins[0]
	assert.Equal(t, "lokaal-plugin", p.Name)
	assert.Equal(t, "2.0.0", p.Version)
	assert.Equal(t, "Geonovum", p.Author)
	assert.Equal(t, []string{"geo"}, p.Tags)
	assert.Equal(t, "lokaal-plugin", res.Name)
	require.Len(t, res.Skills, 1, "alleen skills onder de plugin-map")
	assert.Equal(t, "a", res.Skills[0].Name)
	assert.Equal(t, p.Id, *res.Skills[0].PluginID)
	assert.Contains(t, strings.Join(res.Warnings, "\n"), "kapot")
}

func TestSameSkillGetsSameIDAcrossSources(t *testing.T) {
	h := New(newFakeMarketplace(t))
	market, err := h.Harvest(context.Background(), models.Source{Id: "m", Type: models.SourceTypeMarketplace, Url: "https://github.com/org/market"})
	require.NoError(t, err)
	plugin, err := h.Harvest(context.Background(), models.Source{Id: "p", Type: models.SourceTypePlugin, Url: "https://github.com/org/extern"})
	require.NoError(t, err)
	skill, err := h.Harvest(context.Background(), models.Source{Id: "s", Type: models.SourceTypeSkill, Url: "https://github.com/org/extern/blob/main/skills/c/SKILL.md"})
	require.NoError(t, err)

	var fromMarket models.Skill
	for _, sk := range market.Skills {
		if sk.Name == "c" {
			fromMarket = sk
		}
	}
	assert.Equal(t, fromMarket.Id, plugin.Skills[0].Id)
	assert.Equal(t, fromMarket.Id, skill.Skills[0].Id)
	assert.Equal(t, market.Plugins[1].Id, plugin.Plugins[0].Id, "plugin extern")
}

func TestHarvestPluginWithoutManifestUsesRepoName(t *testing.T) {
	h := New(newFakeMarketplace(t))
	res, err := h.Harvest(context.Background(), models.Source{Id: "src-p", Type: models.SourceTypePlugin, Url: "https://github.com/org/extern"})
	require.NoError(t, err)
	require.Len(t, res.Plugins, 1)
	assert.Equal(t, "extern", res.Plugins[0].Name)
	require.Len(t, res.Skills, 1)
	assert.Equal(t, "c", res.Skills[0].Name)
}

func TestHarvestPluginWithoutSkills(t *testing.T) {
	h := New(newFakeMarketplace(t))
	_, err := h.Harvest(context.Background(), models.Source{Id: "src-p", Type: models.SourceTypePlugin, Url: "https://github.com/org/market/tree/main/bestaat-niet"})
	assert.ErrorContains(t, err, "geen SKILL.md")
}

func TestHarvestSingleSkill(t *testing.T) {
	h := New(newFakeMarketplace(t))
	res, err := h.Harvest(context.Background(), models.Source{Id: "src-s", Type: models.SourceTypeSkill, Url: "https://github.com/org/market/blob/main/plugins/ander/skills/b/SKILL.md"})
	require.NoError(t, err)
	assert.Empty(t, res.Plugins)
	require.Len(t, res.Skills, 1)
	assert.Nil(t, res.Skills[0].PluginID)
	assert.Equal(t, "b", res.Skills[0].Name)
	assert.Equal(t, "b", res.Name)

	// Een map met één skill werkt ook.
	res, err = h.Harvest(context.Background(), models.Source{Id: "src-s", Type: models.SourceTypeSkill, Url: "https://github.com/org/market/tree/main/plugins/ander/skills/b"})
	require.NoError(t, err)
	require.Len(t, res.Skills, 1)
}

func TestHarvestSkillsInDirectory(t *testing.T) {
	h := New(newFakeMarketplace(t))
	res, err := h.Harvest(context.Background(), models.Source{Id: "src-s", Type: models.SourceTypeSkill, Url: "https://github.com/org/market/tree/main/plugins"})
	require.NoError(t, err)
	assert.Empty(t, res.Plugins)
	require.Len(t, res.Skills, 2, "a en b, kapot geeft een waarschuwing")
	assert.Equal(t, "org/market/plugins", res.Name)
}

func TestHarvestBrokenSingleSkill(t *testing.T) {
	h := New(newFakeMarketplace(t))
	_, err := h.Harvest(context.Background(), models.Source{Id: "src-s", Type: models.SourceTypeSkill, Url: "https://github.com/org/market/blob/main/plugins/lokaal/skills/kapot/SKILL.md"})
	assert.ErrorContains(t, err, "geen geldige SKILL.md")
}

func TestHarvestMarketplaceInSubdirectory(t *testing.T) {
	manifest := `{"name":"sub","plugins":[{"name":"p","source":"./p"}]}`
	repos := &fakeRepos{
		commits: map[string]string{"org/mono": marketSHA},
		files: map[string]map[string]string{"org/mono": {
			"market/.claude-plugin/marketplace.json": manifest,
			"market/p/skills/x/SKILL.md":             skillMD("x"),
			"p/skills/y/SKILL.md":                    skillMD("y"),
		}},
	}
	res, err := New(repos).Harvest(context.Background(), models.Source{Id: "m", Type: models.SourceTypeMarketplace, Url: "https://github.com/org/mono/tree/main/market"})
	require.NoError(t, err)
	require.Len(t, res.Skills, 1)
	assert.Equal(t, "x", res.Skills[0].Name, "relatief pad vanaf de marketplace-root")
}

func TestHarvestMarketplaceWithoutManifest(t *testing.T) {
	h := New(newFakeMarketplace(t))
	_, err := h.Harvest(context.Background(), models.Source{Id: "x", Type: models.SourceTypeMarketplace, Url: "https://github.com/org/extern"})
	assert.ErrorContains(t, err, "marketplace.json")
}

func TestResolvePluginSource(t *testing.T) {
	market := RepoRef{Owner: "org", Repo: "market"}
	cases := []struct {
		name    string
		raw     string
		want    pluginLocation
		wantErr bool
	}{
		{"relatief", `"./plugins/x"`, pluginLocation{repo: market, ref: marketSHA, dir: "plugins/x"}, false},
		{"root", `"./"`, pluginLocation{repo: market, ref: marketSHA}, false},
		{"buiten repo", `"./../x"`, pluginLocation{}, true},
		{"zonder ./", `"plugins/x"`, pluginLocation{}, true},
		{"github", `{"source":"github","repo":"a/b","ref":"v1"}`, pluginLocation{repo: RepoRef{"a", "b"}, ref: "v1"}, false},
		{"url", `{"source":"url","url":"https://github.com/a/b.git"}`, pluginLocation{repo: RepoRef{"a", "b"}}, false},
		{"git-subdir", `{"source":"git-subdir","url":"https://github.com/a/b","path":"p","sha":"abc"}`, pluginLocation{repo: RepoRef{"a", "b"}, ref: "abc", dir: "p"}, false},
		{"gitlab", `{"source":"url","url":"https://gitlab.com/a/b"}`, pluginLocation{}, true},
		{"npm", `{"source":"npm","package":"x"}`, pluginLocation{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePluginSource(json.RawMessage(tc.raw), market, marketSHA, "")
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
