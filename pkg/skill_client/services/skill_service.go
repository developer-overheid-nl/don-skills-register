package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	commonfilters "github.com/developer-overheid-nl/don-register-common/filters"
	commonhttpclient "github.com/developer-overheid-nl/don-register-common/httpclient"
	commontypesense "github.com/developer-overheid-nl/don-register-common/typesense"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/harvester"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/helpers/problem"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/repositories"
	"github.com/google/uuid"
)

// SourceHarvester haalt de inhoud van een bron op.
type SourceHarvester interface {
	Harvest(ctx context.Context, source models.Source) (*harvester.Result, error)
}

// OrganisationLabeler zoekt het label van een TOOI-organisatie op.
type OrganisationLabeler func(ctx context.Context, uri string) (string, error)

type SkillService struct {
	repo      repositories.SkillsRepository
	harvester SourceHarvester
	labeler   OrganisationLabeler
	indexer   func(ctx context.Context, skill models.Skill) error

	// harvestMu voorkomt dat de dagelijkse job en een handmatige registratie
	// tegelijk dezelfde bron harvesten.
	harvestMu sync.Mutex
}

func NewSkillService(repo repositories.SkillsRepository, h SourceHarvester) *SkillService {
	return &SkillService{
		repo:      repo,
		harvester: h,
		labeler: func(ctx context.Context, uri string) (string, error) {
			return commonhttpclient.FetchOrganisationLabel(ctx, uri)
		},
		indexer: publishSkillToTypesense,
	}
}

// SetOrganisationLabeler vervangt de TOOI-lookup, bijvoorbeeld in tests.
func (s *SkillService) SetOrganisationLabeler(l OrganisationLabeler) {
	s.labeler = l
}

// ---- Skills ----

func (s *SkillService) ListSkills(ctx context.Context, p *models.ListSkillsParams) ([]models.SkillSummary, models.Pagination, error) {
	skills, pagination, err := s.repo.ListSkills(ctx, p.Page, p.PerPage, &p.SkillFiltersParams)
	if err != nil {
		return nil, models.Pagination{}, err
	}
	out := make([]models.SkillSummary, len(skills))
	for i := range skills {
		out[i] = toSkillSummary(&skills[i])
	}
	return out, pagination, nil
}

func (s *SkillService) RetrieveSkill(ctx context.Context, id string) (*models.SkillDetail, error) {
	skill, err := s.getSkill(ctx, id)
	if err != nil {
		return nil, err
	}
	return &models.SkillDetail{
		SkillSummary: toSkillSummary(skill),
		Ref:          skill.Ref,
		Frontmatter:  skill.Frontmatter,
		Content:      skill.Content,
	}, nil
}

// RetrieveSkillContent geeft de ruwe SKILL.md.
func (s *SkillService) RetrieveSkillContent(ctx context.Context, id string) (string, error) {
	skill, err := s.getSkill(ctx, id)
	if err != nil {
		return "", err
	}
	return skill.Content, nil
}

func (s *SkillService) getSkill(ctx context.Context, id string) (*models.Skill, error) {
	skill, err := s.repo.GetSkillByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if skill == nil {
		return nil, problem.NewNotFound("Resource does not exist")
	}
	return skill, nil
}

func (s *SkillService) GetSkillFilters(ctx context.Context, p *models.SkillFiltersParams) ([]models.FilterGroup, error) {
	counts, err := s.repo.GetSkillFilterCounts(ctx, p)
	if err != nil {
		return nil, err
	}
	return []models.FilterGroup{
		filterGroup("plugin", "Plugin", "De plugin waar de skill in zit", counts.Plugin, p.Plugin),
		filterGroup("organisation", "Organisatie", "De organisatie achter de bron", counts.Organisation, p.Organisation),
		filterGroup("source", "Bron", "De marketplace of repository waar de skill vandaan komt", counts.Source, p.Source),
	}, nil
}

func filterGroup(key, label, description string, counts []models.FilterCount, selectedValues []string) models.FilterGroup {
	selected := commonfilters.SelectedSet(selectedValues)
	options := make([]models.FilterOption, 0, len(counts))
	for _, c := range counts {
		lbl := c.Label
		if lbl == "" {
			lbl = c.Value
		}
		options = append(options, models.FilterOption{Value: c.Value, Label: lbl, Count: c.Count, Selected: selected[c.Value]})
	}
	options = commonfilters.AppendMissingSelectedOptions(options, selected, func(v string) models.FilterOption {
		return models.FilterOption{Value: v, Label: v, Selected: true}
	})
	commonfilters.SortOptions(options)
	return models.FilterGroup{Key: key, Label: label, Description: description, Type: "multi-select", Options: options}
}

// ---- Plugins ----

func (s *SkillService) ListPlugins(ctx context.Context, p *models.ListParams) ([]models.Plugin, models.Pagination, error) {
	return s.repo.ListPlugins(ctx, p.Page, p.PerPage)
}

func (s *SkillService) RetrievePlugin(ctx context.Context, id string) (*models.Plugin, error) {
	plugin, err := s.repo.GetPluginByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin == nil {
		return nil, problem.NewNotFound("Resource does not exist")
	}
	return plugin, nil
}

// ---- Sources ----

func (s *SkillService) ListSources(ctx context.Context, p *models.ListParams) ([]models.Source, models.Pagination, error) {
	return s.repo.ListSources(ctx, p.Page, p.PerPage)
}

func (s *SkillService) RetrieveSource(ctx context.Context, id string) (*models.Source, error) {
	source, err := s.repo.GetSourceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, problem.NewNotFound("Resource does not exist")
	}
	return source, nil
}

// HarvestAll harvest alle geregistreerde bronnen en geeft het aantal
// opgeslagen skills terug.
func (s *SkillService) HarvestAll(ctx context.Context) (int, error) {
	sources, err := s.repo.AllSources(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for i := range sources {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		total += s.HarvestSource(ctx, &sources[i])
	}
	return total, nil
}

// HarvestSource harvest één bron en slaat het resultaat op. Bij een fout
// blijft de vorige inhoud staan en wordt de fout op de bron bewaard.
func (s *SkillService) HarvestSource(ctx context.Context, source *models.Source) int {
	s.harvestMu.Lock()
	defer s.harvestMu.Unlock()

	now := time.Now().UTC()
	source.LastHarvestedAt = &now

	result, err := s.harvester.Harvest(ctx, *source)
	if err == nil {
		err = s.repo.ReplaceSourceContent(ctx, source.Id, result.Plugins, result.Skills)
	}
	if err != nil {
		log.Printf("[harvest] %s mislukt: %v", source.Url, err)
		source.LastError = err.Error()
		if saveErr := s.repo.SaveSource(ctx, source); saveErr != nil {
			log.Printf("[harvest] status van %s opslaan mislukt: %v", source.Url, saveErr)
		}
		return 0
	}

	for _, w := range result.Warnings {
		log.Printf("[harvest] %s: %s", source.Url, w)
	}
	source.LastError = strings.Join(result.Warnings, "\n")
	if result.Name != "" {
		source.Name = result.Name
	}
	if result.Description != "" {
		source.Description = result.Description
	}
	if err := s.repo.SaveSource(ctx, source); err != nil {
		log.Printf("[harvest] status van %s opslaan mislukt: %v", source.Url, err)
	}
	s.index(result.Plugins, result.Skills)
	log.Printf("[harvest] %s: %d plugins, %d skills", source.Url, len(result.Plugins), len(result.Skills))
	return len(result.Skills)
}

// SyncSources maakt de bronnen in de database gelijk aan de configuratie
// (sources.json): nieuwe bronnen worden toegevoegd, type en organisatie van
// bestaande bronnen bijgewerkt, en bronnen die niet meer in de configuratie
// staan worden met hun plugins en skills verwijderd. Harvesten gebeurt
// daarna door de harvest-job.
func (s *SkillService) SyncSources(ctx context.Context, configs []models.SourceConfig) error {
	wanted := make(map[string]models.SourceConfig, len(configs))
	for _, cfg := range configs {
		if cfg.Type != models.SourceTypeMarketplace && cfg.Type != models.SourceTypeRepository {
			return fmt.Errorf("bron %q: onbekend type %q", cfg.Url, cfg.Type)
		}
		repo, err := harvester.ParseGitHubURL(cfg.Url)
		if err != nil {
			return fmt.Errorf("bron %q: %w", cfg.Url, err)
		}
		if _, dup := wanted[repo.URL()]; dup {
			return fmt.Errorf("bron %q staat dubbel in de configuratie", repo.URL())
		}
		wanted[repo.URL()] = cfg
	}

	existing, err := s.repo.AllSources(ctx)
	if err != nil {
		return err
	}
	byURL := make(map[string]*models.Source, len(existing))
	for i := range existing {
		src := &existing[i]
		if _, ok := wanted[src.Url]; !ok {
			if err := s.repo.DeleteSource(ctx, src.Id); err != nil {
				return err
			}
			log.Printf("[sources] bron %s verwijderd", src.Url)
			continue
		}
		byURL[src.Url] = src
	}

	for url, cfg := range wanted {
		source, found := byURL[url]
		if !found {
			repo, _ := harvester.ParseGitHubURL(url)
			source = &models.Source{Id: uuid.NewString(), Url: url, Name: repo.String(), CreatedAt: time.Now().UTC()}
		}
		source.Type = cfg.Type
		source.OrganisationID = nil
		if uri := strings.TrimSpace(cfg.OrganisationUri); uri != "" {
			org, err := s.ensureOrganisation(ctx, uri)
			if err != nil {
				log.Printf("[sources] organisatie %s voor %s niet gevonden: %v", uri, url, err)
			} else {
				source.OrganisationID = &org.Uri
			}
		}
		source.Organisation = nil
		if err := s.repo.SaveSource(ctx, source); err != nil {
			return err
		}
		if !found {
			log.Printf("[sources] bron %s toegevoegd", url)
		}
	}
	return nil
}

func (s *SkillService) index(plugins []models.Plugin, skills []models.Skill) {
	if s.indexer == nil {
		return
	}
	byID := make(map[string]*models.Plugin, len(plugins))
	for i := range plugins {
		byID[plugins[i].Id] = &plugins[i]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, skill := range skills {
		if skill.PluginID != nil {
			skill.Plugin = byID[*skill.PluginID]
		}
		if err := s.indexer(ctx, skill); err != nil {
			if errors.Is(err, commontypesense.ErrDisabled) {
				return
			}
			log.Printf("[typesense] skill %s indexeren mislukt: %v", skill.Id, err)
		}
	}
}

// ---- Organisations ----

func (s *SkillService) ListOrganisations(ctx context.Context, p *models.ListParams) ([]models.Organisation, models.Pagination, error) {
	return s.repo.ListOrganisations(ctx, p.Page, p.PerPage)
}

func (s *SkillService) CreateOrganisation(ctx context.Context, org *models.Organisation) (*models.Organisation, error) {
	org.Uri = strings.TrimSpace(org.Uri)
	existing, err := s.repo.FindOrganisationByURI(ctx, org.Uri)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, problem.New(http.StatusConflict, "Organisation already exists",
			bodyError("uri", "conflict", "organisation already exists"))
	}
	if lbl, err := s.labeler(ctx, org.Uri); err == nil && strings.TrimSpace(lbl) != "" {
		org.Label = strings.TrimSpace(lbl)
	}
	if strings.TrimSpace(org.Label) == "" {
		return nil, problem.NewBadRequest("Invalid input", bodyError("label", "required", "label is required"))
	}
	if err := s.repo.SaveOrganisation(ctx, org); err != nil {
		return nil, err
	}
	return org, nil
}

// ensureOrganisation zoekt een organisatie op en maakt hem aan via TOOI als
// hij nog niet bestaat.
func (s *SkillService) ensureOrganisation(ctx context.Context, uri string) (*models.Organisation, error) {
	existing, err := s.repo.FindOrganisationByURI(ctx, uri)
	if err != nil || existing != nil {
		return existing, err
	}
	label, err := s.labeler(ctx, uri)
	if err != nil || strings.TrimSpace(label) == "" {
		return nil, problem.NewBadRequest("Invalid input",
			bodyError("organisationUri", "unknown", fmt.Sprintf("organisatie niet gevonden in TOOI: %v", err)))
	}
	org := &models.Organisation{Uri: uri, Label: strings.TrimSpace(label)}
	if err := s.repo.SaveOrganisation(ctx, org); err != nil {
		return nil, err
	}
	return org, nil
}

func toSkillSummary(s *models.Skill) models.SkillSummary {
	out := models.SkillSummary{
		Id:            s.Id,
		Name:          s.Name,
		Description:   s.Description,
		RepoUrl:       s.RepoUrl,
		Path:          s.Path,
		CreatedAt:     s.CreatedAt,
		LastCrawledAt: s.LastCrawled,
	}
	if s.Plugin != nil {
		out.Plugin = &models.PluginRef{Id: s.Plugin.Id, Name: s.Plugin.Name, Version: s.Plugin.Version}
	}
	if s.Source != nil {
		out.Source = &models.SourceRef{Id: s.Source.Id, Type: s.Source.Type, Url: s.Source.Url}
		if org := s.Source.Organisation; org != nil {
			out.Organisation = &models.OrganisationSummary{Uri: org.Uri, Label: org.Label}
		}
	}
	return out
}

func bodyError(field, code, detail string) problem.ErrorDetail {
	return problem.ErrorDetail{In: "body", Location: "#/" + field, Code: code, Detail: detail}
}
