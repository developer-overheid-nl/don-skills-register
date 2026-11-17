package skill_client_test

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	api "github.com/developer-overheid-nl/don-skills-register/pkg/skill_client"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/handler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAPIMatchesRoutes bewaakt dat api/openapi.json precies de routes
// beschrijft die de router aanbiedt.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.json")
	require.NoError(t, err)
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &spec))

	var documented []string
	for path, ops := range spec.Paths {
		for method := range ops {
			if method == "parameters" {
				continue
			}
			documented = append(documented, strings.ToUpper(method)+" /v1"+path)
		}
	}

	param := regexp.MustCompile(`:([A-Za-z]+)`)
	var served []string
	for _, r := range api.NewRouter("0.1.0", handler.NewSkillController(nil)).Engine().Routes() {
		if r.Method == http.MethodHead {
			continue // gin.StaticFile registreert ook HEAD
		}
		served = append(served, r.Method+" "+param.ReplaceAllString(r.Path, "{$1}"))
	}

	require.NotEmpty(t, served)
	sort.Strings(documented)
	sort.Strings(served)
	assert.Equal(t, served, documented)
}
