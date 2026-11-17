package skill_client

import (
	commonrouter "github.com/developer-overheid-nl/don-register-common/router"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/handler"
	"github.com/loopfz/gadgeto/tonic"
	"github.com/wI2L/fizz"
	"github.com/wI2L/fizz/openapi"
)

var apiVersionHeader = fizz.Header("API-Version", "De API-versie van de response", "")

func security(scopes ...string) fizz.OperationOption {
	req := &openapi.SecurityRequirement{"clientCredentials": scopes}
	return fizz.Security(req)
}

func publicSecurity(scopes ...string) fizz.OperationOption {
	req := &openapi.SecurityRequirement{"apiKey": {}, "clientCredentials": scopes}
	return fizz.Security(req)
}

func NewRouter(apiVersion string, controller *handler.SkillController) *fizz.Fizz {
	g := commonrouter.NewEngine(apiVersion, commonrouter.CORSOptions{
		AllowHeaders:  []string{"Origin", "Content-Length", "Content-Type", "Authorization", "API-Version", "X-Api-Key"},
		ExposeHeaders: []string{"API-Version", "Link", "Total-Count", "Total-Pages", "Per-Page", "Current-Page"},
	})
	commonrouter.InstallProblemHandlers(g, apiVersion)
	f := fizz.NewFromEngine(g)

	root := f.Group("/v1", "Skills v1", "Skills Register V1 routes")

	// Skills
	root.GET("/skills", []fizz.OperationOption{
		fizz.ID("listSkills"),
		fizz.Summary("List skills"),
		fizz.Description("Geeft een lijst met AI skills uit alle geharvestte bronnen. Ondersteunt dezelfde filters als het filterendpoint en de zoekterm q."),
		publicSecurity("skills:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.ListSkills, 200))

	root.GET("/skills/filters", []fizz.OperationOption{
		fizz.ID("listSkillFilters"),
		fizz.Summary("Filter opties ophalen"),
		fizz.Description("Geeft alle beschikbare filteropties terug met counts, berekend op basis van de actieve filters en de zoekterm q."),
		publicSecurity("skills:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.ListSkillFilters, 200))

	root.GET("/skills/:id", []fizz.OperationOption{
		fizz.ID("getSkillById"),
		fizz.Summary("Get skill by id"),
		fizz.Description("Geeft één skill terug, inclusief frontmatter en de inhoud van SKILL.md."),
		publicSecurity("skills:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.RetrieveSkill, 200))

	// Ruwe SKILL.md; op de engine zodat de handler het content type bepaalt.
	g.GET("/v1/skills/:id/skill.md", controller.RetrieveSkillContent)

	// Plugins
	root.GET("/plugins", []fizz.OperationOption{
		fizz.ID("listPlugins"),
		fizz.Summary("List plugins"),
		fizz.Description("Geeft alle plugins uit de geharvestte marketplaces."),
		publicSecurity("skills:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.ListPlugins, 200))

	root.GET("/plugins/:id", []fizz.OperationOption{
		fizz.ID("getPluginById"),
		fizz.Summary("Get plugin by id"),
		fizz.Description("Geeft één plugin terug."),
		publicSecurity("skills:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.RetrievePlugin, 200))

	// Sources
	root.GET("/sources", []fizz.OperationOption{
		fizz.ID("listSources"),
		fizz.Summary("List sources"),
		fizz.Description("Geeft alle geregistreerde bronnen (marketplaces en losse repositories) met de status van de laatste harvest."),
		publicSecurity("sources:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.ListSources, 200))

	root.GET("/sources/:id", []fizz.OperationOption{
		fizz.ID("getSourceById"),
		fizz.Summary("Get source by id"),
		fizz.Description("Geeft één bron terug."),
		publicSecurity("sources:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.RetrieveSource, 200))


	// Organisations
	root.GET("/organisations", []fizz.OperationOption{
		fizz.ID("listOrganisations"),
		fizz.Summary("Alle organisaties ophalen"),
		fizz.Description("Alle organisaties ophalen"),
		publicSecurity("organisations:read"),
		apiVersionHeader,
	}, tonic.Handler(controller.ListOrganisations, 200))

	root.POST("/organisations", []fizz.OperationOption{
		fizz.ID("createOrganisation"),
		fizz.Summary("Organisatie aanmaken"),
		fizz.Description("Maak een nieuwe organisatie aan."),
		security("organisations:write"),
		apiVersionHeader,
	}, tonic.Handler(controller.CreateOrganisation, 201))

	g.StaticFile("/v1/openapi.json", "./api/openapi.json")

	return f
}
