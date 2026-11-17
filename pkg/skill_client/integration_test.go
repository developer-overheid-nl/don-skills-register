package skill_client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/developer-overheid-nl/don-skills-register/pkg/skill_client"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/database"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/handler"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/harvester"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/repositories"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/services"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const orgURI = "https://identifier.overheid.nl/tooi/id/oorg/oorg10103"

// stubHarvester geeft per aanroep het ingestelde resultaat terug.
type stubHarvester struct {
	result *harvester.Result
	err    error
}

func (s *stubHarvester) Harvest(_ context.Context, source models.Source) (*harvester.Result, error) {
	if s.err != nil {
		return nil, s.err
	}
	// Kopie met het echte bron-id, zoals de echte harvester doet.
	out := *s.result
	out.Plugins = append([]models.Plugin(nil), s.result.Plugins...)
	out.Skills = append([]models.Skill(nil), s.result.Skills...)
	for i := range out.Plugins {
		out.Plugins[i].SourceID = source.Id
	}
	for i := range out.Skills {
		out.Skills[i].SourceID = source.Id
	}
	return &out, nil
}

func strPtr(s string) *string { return &s }

func setup(t *testing.T, h *stubHarvester) (http.Handler, *services.SkillService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("ENABLE_TYPESENSE", "false")

	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(database.Models...))

	svc := services.NewSkillService(repositories.NewSkillsRepository(db), h)
	svc.SetOrganisationLabeler(func(context.Context, string) (string, error) { return "Geonovum", nil })
	return api.NewRouter("0.1.0", handler.NewSkillController(svc)), svc
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

// syncAndHarvest zet de bronnen zoals in sources.json en draait de
// harvest-job één keer.
func syncAndHarvest(t *testing.T, svc *services.SkillService, sources ...models.SourceConfig) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.SyncSources(ctx, sources))
	_, err := svc.HarvestAll(ctx)
	require.NoError(t, err)
}

func listSources(t *testing.T, router http.Handler) []models.Source {
	t.Helper()
	rec := do(t, router, http.MethodGet, "/v1/sources", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[[]models.Source](t, rec)
}

func TestHarvestAndBrowseSkills(t *testing.T) {
	stub := &stubHarvester{result: &harvester.Result{
		Name: "overheid-plugins",
		Plugins: []models.Plugin{
			{Id: "p1", Name: "standaarden", Version: "1.0.0", Tags: []string{"api"}},
		},
		Skills: []models.Skill{
			{Id: "s1", PluginID: strPtr("p1"), Name: "ls-api", Description: "API Design Rules", Path: "skills/ls-api/SKILL.md", RepoUrl: "https://github.com/org/standaarden", Content: "---\nname: ls-api\n---\nBody"},
			{Id: "s2", PluginID: strPtr("p1"), Name: "ls-dk", Description: "Digikoppeling", Path: "skills/ls-dk/SKILL.md", RepoUrl: "https://github.com/org/standaarden"},
		},
	}}
	router, svc := setup(t, stub)
	syncAndHarvest(t, svc, models.SourceConfig{Type: models.SourceTypeMarketplace, Url: "https://github.com/org/market.git", OrganisationUri: orgURI})

	sources := listSources(t, router)
	require.Len(t, sources, 1)
	source := decode[models.Source](t, do(t, router, http.MethodGet, "/v1/sources/"+sources[0].Id, nil))
	assert.Equal(t, "https://github.com/org/market", source.Url)
	assert.Equal(t, "overheid-plugins", source.Name)
	assert.NotNil(t, source.LastHarvestedAt)
	require.NotNil(t, source.Organisation)
	assert.Equal(t, "Geonovum", source.Organisation.Label)

	// Bronnen kunnen niet via de API worden toegevoegd.
	rec := do(t, router, http.MethodPost, "/v1/sources", map[string]any{"type": "marketplace", "url": "https://github.com/org/x"})
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	// Lijst
	rec = do(t, router, http.MethodGet, "/v1/skills?perPage=1", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "2", rec.Header().Get("Total-Count"))
	list := decode[[]models.SkillSummary](t, rec)
	require.Len(t, list, 1)
	assert.Equal(t, "ls-api", list[0].Name)
	require.NotNil(t, list[0].Plugin)
	assert.Equal(t, "standaarden", list[0].Plugin.Name)
	require.NotNil(t, list[0].Organisation)
	assert.Equal(t, orgURI, list[0].Organisation.Uri)

	// Ongeldige paginering
	for _, path := range []string{"/v1/skills?perPage=1000", "/v1/plugins?page=abc", "/v1/sources?perPage=-1", "/v1/organisations?perPage=101"} {
		rec = do(t, router, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
	}

	// Zoeken
	rec = do(t, router, http.MethodGet, "/v1/skills?q=digikoppeling", nil)
	assert.Len(t, decode[[]models.SkillSummary](t, rec), 1)

	// Detail en ruwe SKILL.md
	rec = do(t, router, http.MethodGet, "/v1/skills/s1", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, decode[models.SkillDetail](t, rec).Content, "Body")

	rec = do(t, router, http.MethodGet, "/v1/skills/s1/skill.md", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/markdown; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "name: ls-api")

	rec = do(t, router, http.MethodGet, "/v1/skills/bestaat-niet", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = do(t, router, http.MethodGet, "/v1/skills/bestaat-niet/skill.md", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	// Filters
	rec = do(t, router, http.MethodGet, "/v1/skills/filters?plugin=p1", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	groups := decode[[]models.FilterGroup](t, rec)
	require.Len(t, groups, 3)
	assert.Equal(t, "plugin", groups[0].Key)
	require.Len(t, groups[0].Options, 1)
	assert.Equal(t, 2, groups[0].Options[0].Count)
	assert.True(t, groups[0].Options[0].Selected)

	// Plugins
	rec = do(t, router, http.MethodGet, "/v1/plugins", nil)
	assert.Len(t, decode[[]models.Plugin](t, rec), 1)
	rec = do(t, router, http.MethodGet, "/v1/plugins/p1", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReharvestRemovesSkillsAndKeepsCreatedAt(t *testing.T) {
	stub := &stubHarvester{result: &harvester.Result{
		Skills: []models.Skill{
			{Id: "s1", Name: "een", Path: "a/SKILL.md"},
			{Id: "s2", Name: "twee", Path: "b/SKILL.md"},
		},
	}}
	router, svc := setup(t, stub)
	cfg := models.SourceConfig{Type: models.SourceTypeRepository, Url: "https://github.com/org/losse-skills"}
	syncAndHarvest(t, svc, cfg)
	before := decode[models.SkillDetail](t, do(t, router, http.MethodGet, "/v1/skills/s1", nil))

	stub.result.Skills = stub.result.Skills[:1]
	syncAndHarvest(t, svc, cfg)

	list := decode[[]models.SkillSummary](t, do(t, router, http.MethodGet, "/v1/skills", nil))
	require.Len(t, list, 1)
	assert.Equal(t, "s1", list[0].Id)
	assert.True(t, before.CreatedAt.Equal(list[0].CreatedAt))
}

func TestSyncSourcesFollowsConfig(t *testing.T) {
	stub := &stubHarvester{result: &harvester.Result{Skills: []models.Skill{{Id: "s1", Name: "een", Path: "a/SKILL.md"}}}}
	router, svc := setup(t, stub)
	a := models.SourceConfig{Type: models.SourceTypeMarketplace, Url: "https://github.com/org/a"}
	b := models.SourceConfig{Type: models.SourceTypeRepository, Url: "https://github.com/org/b"}

	require.NoError(t, svc.SyncSources(context.Background(), []models.SourceConfig{a}))
	idA := listSources(t, router)[0].Id
	_, err := svc.HarvestAll(context.Background())
	require.NoError(t, err)

	// Bron a uit de configuratie, b erbij: a en zijn skills verdwijnen.
	require.NoError(t, svc.SyncSources(context.Background(), []models.SourceConfig{b}))
	sources := listSources(t, router)
	require.Len(t, sources, 1)
	assert.Equal(t, "https://github.com/org/b", sources[0].Url)
	assert.NotEqual(t, idA, sources[0].Id)
	assert.Empty(t, decode[[]models.SkillSummary](t, do(t, router, http.MethodGet, "/v1/skills", nil)))

	// Type en organisatie volgen de configuratie, het id blijft gelijk.
	b.Type = models.SourceTypeMarketplace
	b.OrganisationUri = orgURI
	require.NoError(t, svc.SyncSources(context.Background(), []models.SourceConfig{b}))
	updated := listSources(t, router)
	require.Len(t, updated, 1)
	assert.Equal(t, sources[0].Id, updated[0].Id)
	assert.Equal(t, models.SourceTypeMarketplace, updated[0].Type)
	require.NotNil(t, updated[0].Organisation)
	assert.Equal(t, orgURI, updated[0].Organisation.Uri)
}

func TestSyncSourcesRejectsInvalidConfig(t *testing.T) {
	_, svc := setup(t, &stubHarvester{result: &harvester.Result{}})
	ctx := context.Background()

	assert.ErrorContains(t, svc.SyncSources(ctx, []models.SourceConfig{{Type: "onbekend", Url: "https://github.com/org/x"}}), "onbekend type")
	assert.ErrorContains(t, svc.SyncSources(ctx, []models.SourceConfig{{Type: models.SourceTypeRepository, Url: "https://gitlab.com/org/x"}}), "github.com")
	assert.ErrorContains(t, svc.SyncSources(ctx, []models.SourceConfig{
		{Type: models.SourceTypeRepository, Url: "https://github.com/org/x"},
		{Type: models.SourceTypeRepository, Url: "https://github.com/org/x.git"},
	}), "dubbel")
}

func TestHarvestErrorIsStoredOnSource(t *testing.T) {
	router, svc := setup(t, &stubHarvester{err: assert.AnError})
	syncAndHarvest(t, svc, models.SourceConfig{Type: models.SourceTypeMarketplace, Url: "https://github.com/org/kapot"})

	sources := listSources(t, router)
	require.Len(t, sources, 1)
	assert.Equal(t, assert.AnError.Error(), sources[0].LastError)
}

func TestOrganisations(t *testing.T) {
	router, _ := setup(t, &stubHarvester{result: &harvester.Result{}})

	rec := do(t, router, http.MethodPost, "/v1/organisations", map[string]any{"uri": orgURI})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	rec = do(t, router, http.MethodPost, "/v1/organisations", map[string]any{"uri": orgURI})
	assert.Equal(t, http.StatusConflict, rec.Code)

	rec = do(t, router, http.MethodGet, "/v1/organisations", nil)
	orgs := decode[[]models.Organisation](t, rec)
	require.Len(t, orgs, 1)
	assert.Equal(t, "Geonovum", orgs[0].Label)
}
