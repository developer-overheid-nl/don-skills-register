package handler

import (
	"errors"
	"net/http"

	commonpagination "github.com/developer-overheid-nl/don-register-common/pagination"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/helpers/problem"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/services"
	"github.com/gin-gonic/gin"
)

type SkillController struct {
	Service *services.SkillService
}

func NewSkillController(s *services.SkillService) *SkillController {
	return &SkillController{Service: s}
}

// ListSkills handles GET /skills
func (c *SkillController) ListSkills(ctx *gin.Context, p *models.ListSkillsParams) ([]models.SkillSummary, error) {
	p.Page, p.PerPage = normalizePagination(p.Page, p.PerPage)
	skills, pagination, err := c.Service.ListSkills(ctx.Request.Context(), p)
	if err != nil {
		return nil, err
	}
	commonpagination.SetHeaders(ctx.Request, ctx.Header, pagination)
	return skills, nil
}

// ListSkillFilters handles GET /skills/filters
func (c *SkillController) ListSkillFilters(ctx *gin.Context, p *models.SkillFiltersParams) ([]models.FilterGroup, error) {
	return c.Service.GetSkillFilters(ctx.Request.Context(), p)
}

// RetrieveSkill handles GET /skills/:id
func (c *SkillController) RetrieveSkill(ctx *gin.Context, p *models.IdParams) (*models.SkillDetail, error) {
	return c.Service.RetrieveSkill(ctx.Request.Context(), p.Id)
}

// RetrieveSkillContent handles GET /skills/:id/skill.md en geeft de ruwe
// markdown terug. Geregistreerd zonder tonic zodat het content type klopt.
func (c *SkillController) RetrieveSkillContent(ctx *gin.Context) {
	content, err := c.Service.RetrieveSkillContent(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		var p problem.ProblemJSON
		if !errors.As(err, &p) {
			p = problem.NewInternalServerError("Internal server error")
		}
		ctx.Header("Content-Type", "application/problem+json")
		ctx.JSON(p.Status, p)
		return
	}
	ctx.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(content))
}

// ListPlugins handles GET /plugins
func (c *SkillController) ListPlugins(ctx *gin.Context, p *models.ListParams) ([]models.Plugin, error) {
	p.Page, p.PerPage = normalizePagination(p.Page, p.PerPage)
	plugins, pagination, err := c.Service.ListPlugins(ctx.Request.Context(), p)
	if err != nil {
		return nil, err
	}
	commonpagination.SetHeaders(ctx.Request, ctx.Header, pagination)
	return plugins, nil
}

// RetrievePlugin handles GET /plugins/:id
func (c *SkillController) RetrievePlugin(ctx *gin.Context, p *models.IdParams) (*models.Plugin, error) {
	return c.Service.RetrievePlugin(ctx.Request.Context(), p.Id)
}

// ListSources handles GET /sources
func (c *SkillController) ListSources(ctx *gin.Context, p *models.ListParams) ([]models.Source, error) {
	p.Page, p.PerPage = normalizePagination(p.Page, p.PerPage)
	sources, pagination, err := c.Service.ListSources(ctx.Request.Context(), p)
	if err != nil {
		return nil, err
	}
	commonpagination.SetHeaders(ctx.Request, ctx.Header, pagination)
	return sources, nil
}

// RetrieveSource handles GET /sources/:id
func (c *SkillController) RetrieveSource(ctx *gin.Context, p *models.IdParams) (*models.Source, error) {
	return c.Service.RetrieveSource(ctx.Request.Context(), p.Id)
}


// ListOrganisations handles GET /organisations
func (c *SkillController) ListOrganisations(ctx *gin.Context, p *models.ListParams) ([]models.Organisation, error) {
	p.Page, p.PerPage = normalizePagination(p.Page, p.PerPage)
	orgs, pagination, err := c.Service.ListOrganisations(ctx.Request.Context(), p)
	if err != nil {
		return nil, err
	}
	commonpagination.SetHeaders(ctx.Request, ctx.Header, pagination)
	return orgs, nil
}

// CreateOrganisation handles POST /organisations
func (c *SkillController) CreateOrganisation(ctx *gin.Context, body *models.Organisation) (*models.Organisation, error) {
	return c.Service.CreateOrganisation(ctx.Request.Context(), body)
}

func normalizePagination(page, perPage int) (int, int) {
	return commonpagination.Normalize(page, perPage, 20, 100)
}
