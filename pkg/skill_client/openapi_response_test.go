package skill_client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/harvester"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	"github.com/stretchr/testify/require"
)

// TestResponsesMatchOpenAPI controleert dat echte responses van de API
// voldoen aan de schema's, statuscodes en headers in api/openapi.json.
func TestResponsesMatchOpenAPI(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.json")
	require.NoError(t, err)
	doc, err := libopenapi.NewDocument(spec)
	require.NoError(t, err)
	v, errs := validator.NewValidator(doc)
	require.Empty(t, errs)

	stub := &stubHarvester{result: &harvester.Result{
		Name:        "overheid-plugins",
		Description: "Plugins van de overheid",
		Plugins: []models.Plugin{
			{Id: "p1", Name: "standaarden", DisplayName: "Standaarden", Version: "1.0.0", Author: "Geonovum", RepoUrl: "https://github.com/org/standaarden", Category: "development", Tags: []string{"api"}},
		},
		Skills: []models.Skill{
			{Id: "s1", PluginID: strPtr("p1"), Name: "ls-api", Description: "API Design Rules", Path: "skills/ls-api/SKILL.md", RepoUrl: "https://github.com/org/standaarden", Ref: "abc", Frontmatter: map[string]any{"name": "ls-api", "description": "API Design Rules"}, Content: "---\nname: ls-api\n---\nBody"},
		},
		Warnings: []string{"plugin npm-ding: npm wordt niet ondersteund"},
	}}
	router, _ := setup(t, stub)
	sourceID := createSource(t, router, models.SourceTypeMarketplace, "https://github.com/org/market").Id

	cases := []struct {
		method, path string
		body         any
		status       int
	}{
		{http.MethodGet, "/skills", nil, http.StatusOK},
		{http.MethodGet, "/skills?q=api&plugin=p1&organisation=" + orgURI, nil, http.StatusOK},
		{http.MethodGet, "/skills?perPage=1000", nil, http.StatusBadRequest},
		{http.MethodGet, "/skills/filters", nil, http.StatusOK},
		{http.MethodGet, "/skills/s1", nil, http.StatusOK},
		{http.MethodGet, "/skills/bestaat-niet", nil, http.StatusNotFound},
		{http.MethodGet, "/skills/s1/skill.md", nil, http.StatusOK},
		{http.MethodGet, "/skills/bestaat-niet/skill.md", nil, http.StatusNotFound},
		{http.MethodGet, "/sources", nil, http.StatusOK},
		{http.MethodGet, "/sources/" + sourceID, nil, http.StatusOK},
		{http.MethodGet, "/sources/bestaat-niet", nil, http.StatusNotFound},
		{http.MethodPost, "/sources", map[string]any{"type": "plugin", "url": "https://github.com/org/repo/tree/main/plugins/geo", "organisationUri": orgURI}, http.StatusCreated},
		{http.MethodPost, "/sources", map[string]any{"type": "marketplace", "url": "https://github.com/org/market", "organisationUri": orgURI}, http.StatusConflict},
		{http.MethodPost, "/sources", map[string]any{"type": "repository", "url": "https://github.com/org/x", "organisationUri": orgURI}, http.StatusBadRequest},
		{http.MethodGet, "/organisations", nil, http.StatusOK},
		{http.MethodPost, "/organisations", map[string]any{"uri": "https://identifier.overheid.nl/tooi/id/oorg/oorg10000"}, http.StatusCreated},
		{http.MethodPost, "/organisations", map[string]any{"uri": orgURI}, http.StatusConflict},
		{http.MethodPost, "/organisations", map[string]any{"uri": "geen-url"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := do(t, router, tc.method, "/v1"+tc.path, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())

			// Valideer tegen de server-URL uit de spec.
			req := httptest.NewRequestWithContext(context.Background(), tc.method, "https://api.developer.overheid.nl/skills-register/v1"+tc.path, nil)
			req.Header.Set("Content-Type", "application/json")
			ok, verrs := v.ValidateHttpResponse(req, rec.Result())
			for _, e := range verrs {
				t.Errorf("%s: %s", e.Message, e.Reason)
				for _, se := range e.SchemaValidationErrors {
					t.Errorf("  %s: %s", se.Location, se.Reason)
				}
			}
			require.True(t, ok)
		})
	}
}
