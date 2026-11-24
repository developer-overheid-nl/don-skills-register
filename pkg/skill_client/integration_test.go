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
	// byType geeft per brontype een ander resultaat dan result.
	byType map[models.SourceType]*harvester.Result
	err    error
}

func (s *stubHarvester) Harvest(_ context.Context, source models.Source) (*harvester.Result, error) {
	if s.err != nil {
		return nil, s.err
	}
	result := s.result
	if r, ok := s.byType[source.Type]; ok {
		result = r
	}
	// Kopie met het echte bron-id, zoals de echte harvester doet.
	out := *result
	out.Plugins = append([]models.Plugin(nil), result.Plugins...)
	out.Skills = append([]models.Skill(nil), result.Skills...)
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
	// Eerste harvest synchroon, zodat tests direct het resultaat zien.
	svc.SetBackgroundRunner(func(f func()) { f() })
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

// createSource registreert een bron via de API; de eerste harvest draait in
// tests synchroon.
func createSource(t *testing.T, router http.Handler, typ models.SourceType, url string) models.Source {
	t.Helper()
	rec := do(t, router, http.MethodPost, "/v1/sources", map[string]any{"type": typ, "url": url, "organisationUri": orgURI})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return decode[models.Source](t, rec)
}

func harvestJob(t *testing.T, svc *services.SkillService) {
	t.Helper()
	_, err := svc.HarvestAll(context.Background())
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
	router, _ := setup(t, stub)
	created := createSource(t, router, models.SourceTypeMarketplace, "https://github.com/org/market.git")
	assert.Equal(t, "https://github.com/org/market", created.Url)
	require.NotNil(t, created.Organisation)
	assert.Equal(t, orgURI, created.Organisation.Uri)

	sources := listSources(t, router)
	require.Len(t, sources, 1)
	source := decode[models.Source](t, do(t, router, http.MethodGet, "/v1/sources/"+sources[0].Id, nil))
	assert.Equal(t, "https://github.com/org/market", source.Url)
	assert.Equal(t, "overheid-plugins", source.Name)
	assert.NotNil(t, source.LastHarvestedAt, "direct na registratie geharvest")
	require.NotNil(t, source.Organisation)
	assert.Equal(t, "Geonovum", source.Organisation.Label)

	// Lijst
	rec := do(t, router, http.MethodGet, "/v1/skills?perPage=1", nil)
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
	for _, path := range []string{"/v1/skills?perPage=1000", "/v1/skills?page=abc", "/v1/sources?perPage=-1", "/v1/organisations?perPage=101"} {
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

	// Geen aparte plugin-endpoints: het is een skills-register.
	rec = do(t, router, http.MethodGet, "/v1/plugins", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestReharvestRemovesSkillsAndKeepsCreatedAt(t *testing.T) {
	stub := &stubHarvester{result: &harvester.Result{
		Skills: []models.Skill{
			{Id: "s1", Name: "een", Path: "a/SKILL.md"},
			{Id: "s2", Name: "twee", Path: "b/SKILL.md"},
		},
	}}
	router, svc := setup(t, stub)
	createSource(t, router, models.SourceTypeSkill, "https://github.com/org/losse-skills")
	before := decode[models.SkillDetail](t, do(t, router, http.MethodGet, "/v1/skills/s1", nil))

	stub.result.Skills = stub.result.Skills[:1]
	harvestJob(t, svc)

	list := decode[[]models.SkillSummary](t, do(t, router, http.MethodGet, "/v1/skills", nil))
	require.Len(t, list, 1)
	assert.Equal(t, "s1", list[0].Id)
	assert.True(t, before.CreatedAt.Equal(list[0].CreatedAt))
}

// Dezelfde skill via twee bronnen staat er één keer in, bij de meest
// specifieke bron.
func TestSkillIsStoredOnceAcrossSources(t *testing.T) {
	stub := &stubHarvester{result: &harvester.Result{
		Plugins: []models.Plugin{{Id: "p1", Name: "internet"}},
		Skills: []models.Skill{
			{Id: "s1", PluginID: strPtr("p1"), Name: "inet", Path: "skills/inet/SKILL.md"},
			{Id: "s2", PluginID: strPtr("p1"), Name: "inet-api", Path: "skills/inet-api/SKILL.md"},
		},
	}}
	router, svc := setup(t, stub)
	market := createSource(t, router, models.SourceTypeMarketplace, "https://github.com/org/market")
	before := decode[models.SkillDetail](t, do(t, router, http.MethodGet, "/v1/skills/s1", nil))

	// Dezelfde plugin los registreren, met een andere organisatie.
	otherOrg := "https://identifier.overheid.nl/tooi/id/oorg/oorg10000"
	rec := do(t, router, http.MethodPost, "/v1/sources", map[string]any{"type": "plugin", "url": "https://github.com/org/internet", "organisationUri": otherOrg})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	plugin := decode[models.Source](t, rec)

	// Een latere tweede marketplace met dezelfde plugin neemt niets over.
	createSource(t, router, models.SourceTypeMarketplace, "https://github.com/org/market-2")
	harvestJob(t, svc)

	list := decode[[]models.SkillSummary](t, do(t, router, http.MethodGet, "/v1/skills", nil))
	require.Len(t, list, 2, "geen dubbele skills")
	for _, sk := range list {
		require.NotNil(t, sk.Source)
		assert.Equal(t, plugin.Id, sk.Source.Id, "de losse plugin gaat voor de marketplace")
		require.NotNil(t, sk.Organisation)
		assert.Equal(t, otherOrg, sk.Organisation.Uri)
	}
	groups := decode[[]models.FilterGroup](t, do(t, router, http.MethodGet, "/v1/skills/filters", nil))
	require.Equal(t, "plugin", groups[0].Key)
	assert.Len(t, groups[0].Options, 1, "plugin staat er één keer in")
	after := decode[models.SkillDetail](t, do(t, router, http.MethodGet, "/v1/skills/s1", nil))
	assert.True(t, before.CreatedAt.Equal(after.CreatedAt), "createdAt blijft bij overname")
	require.NotNil(t, after.Plugin)
	assert.Equal(t, "internet", after.Plugin.Name)
	require.Len(t, after.Marketplaces, 2, "de marketplaces blijven zichtbaar na overname")
	assert.Equal(t, market.Id, after.Marketplaces[0].Id)
	assert.Equal(t, "https://github.com/org/market", after.Marketplaces[0].Url)

	// De marketplace houdt geen skills meer over, maar faalt niet.
	rec = do(t, router, http.MethodGet, "/v1/skills?source="+market.Id, nil)
	assert.Equal(t, "0", rec.Header().Get("Total-Count"))
	assert.Empty(t, decode[models.Source](t, do(t, router, http.MethodGet, "/v1/sources/"+market.Id, nil)).LastError)
}

func TestSkillShowsPluginAndMarketplaces(t *testing.T) {
	stub := &stubHarvester{
		result: &harvester.Result{
			Name:    "overheid-plugins",
			Plugins: []models.Plugin{{Id: "p1", Name: "internet", Version: "1.2.0"}},
			Skills: []models.Skill{
				{Id: "s1", PluginID: strPtr("p1"), Name: "inet", Path: "skills/inet/SKILL.md"},
				{Id: "s2", PluginID: strPtr("p1"), Name: "inet-api", Path: "skills/inet-api/SKILL.md"},
			},
		},
		byType: map[models.SourceType]*harvester.Result{
			models.SourceTypeSkill: {Skills: []models.Skill{{Id: "s1", Name: "inet", Path: "skills/inet/SKILL.md"}}},
		},
	}
	router, svc := setup(t, stub)
	market := createSource(t, router, models.SourceTypeMarketplace, "https://github.com/org/market")
	skill := createSource(t, router, models.SourceTypeSkill, "https://github.com/org/internet/blob/main/skills/inet/SKILL.md")
	harvestJob(t, svc)

	s1 := decode[models.SkillDetail](t, do(t, router, http.MethodGet, "/v1/skills/s1", nil))
	require.NotNil(t, s1.Source)
	assert.Equal(t, skill.Id, s1.Source.Id, "de losse skill gaat voor de marketplace")
	require.NotNil(t, s1.Plugin, "plugin komt uit de marketplace")
	assert.Equal(t, models.PluginRef{Id: "p1", Name: "internet", Version: "1.2.0"}, *s1.Plugin)
	assert.Equal(t, []models.MarketplaceRef{{Id: market.Id, Name: "overheid-plugins", Url: market.Url}}, s1.Marketplaces)

	list := decode[[]models.SkillSummary](t, do(t, router, http.MethodGet, "/v1/skills", nil))
	require.Len(t, list, 2)
	for _, sk := range list {
		assert.Len(t, sk.Marketplaces, 1, sk.Id)
		if sk.Id == "s2" {
			assert.Equal(t, models.SourceRef{Id: market.Id, Type: models.SourceTypeMarketplace, Name: "overheid-plugins", Url: market.Url}, *sk.Source)
		}
	}

	// Verdwijnt de skill uit de marketplace, dan ook de marketplace bij de skill.
	stub.result.Skills = stub.result.Skills[1:]
	harvestJob(t, svc)
	s1 = decode[models.SkillDetail](t, do(t, router, http.MethodGet, "/v1/skills/s1", nil))
	assert.Nil(t, s1.Plugin)
	assert.Empty(t, s1.Marketplaces)
}

func TestCreateSourceTypes(t *testing.T) {
	router, _ := setup(t, &stubHarvester{result: &harvester.Result{}})
	plugin := createSource(t, router, models.SourceTypePlugin, "https://github.com/org/repo/tree/main/plugins/geo/")
	assert.Equal(t, models.SourceTypePlugin, plugin.Type)
	assert.Equal(t, "https://github.com/org/repo/tree/main/plugins/geo", plugin.Url)
	assert.Equal(t, "org/repo/plugins/geo", plugin.Name)

	skill := createSource(t, router, models.SourceTypeSkill, "https://github.com/org/repo/blob/main/skills/a/SKILL.md")
	assert.Equal(t, models.SourceTypeSkill, skill.Type)
	assert.Equal(t, "https://github.com/org/repo/blob/main/skills/a/SKILL.md", skill.Url)

	assert.Len(t, listSources(t, router), 2)
}

func TestCreateSourceRejectsInvalidInput(t *testing.T) {
	router, svc := setup(t, &stubHarvester{result: &harvester.Result{}})
	createSource(t, router, models.SourceTypeMarketplace, "https://github.com/org/x")

	cases := map[string]struct {
		body   map[string]any
		status int
	}{
		"dubbel":             {map[string]any{"type": "marketplace", "url": "https://github.com/org/x.git", "organisationUri": orgURI}, http.StatusConflict},
		"zonder organisatie": {map[string]any{"type": "marketplace", "url": "https://github.com/org/y"}, http.StatusBadRequest},
		"onbekend type":      {map[string]any{"type": "repository", "url": "https://github.com/org/y", "organisationUri": orgURI}, http.StatusBadRequest},
		"zonder type":        {map[string]any{"url": "https://github.com/org/y", "organisationUri": orgURI}, http.StatusBadRequest},
		"gitlab":             {map[string]any{"type": "skill", "url": "https://gitlab.com/org/y", "organisationUri": orgURI}, http.StatusBadRequest},
		"geen repo-pad":      {map[string]any{"type": "skill", "url": "https://github.com/org/y/issues/1", "organisationUri": orgURI}, http.StatusBadRequest},
	}
	for name, tc := range cases {
		rec := do(t, router, http.MethodPost, "/v1/sources", tc.body)
		assert.Equal(t, tc.status, rec.Code, "%s: %s", name, rec.Body.String())
	}

	// Organisatie die niet in TOOI staat.
	svc.SetOrganisationLabeler(func(context.Context, string) (string, error) { return "", assert.AnError })
	rec := do(t, router, http.MethodPost, "/v1/sources", map[string]any{"type": "skill", "url": "https://github.com/org/z", "organisationUri": "https://identifier.overheid.nl/tooi/id/oorg/onbekend"})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Len(t, listSources(t, router), 1)
}

func TestHarvestErrorIsStoredOnSource(t *testing.T) {
	router, _ := setup(t, &stubHarvester{err: assert.AnError})
	createSource(t, router, models.SourceTypeMarketplace, "https://github.com/org/kapot")

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
