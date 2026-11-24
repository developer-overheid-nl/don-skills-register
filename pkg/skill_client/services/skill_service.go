package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
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

	// background voert de eerste harvest van een nieuwe bron uit.
	background func(func())

	// harvestMu voorkomt dat de dagelijkse job en een nieuwe registratie
	// tegelijk harvesten.
	harvestMu sync.Mutex
}

const firstHarvestTimeout = 10 * time.Minute

func NewSkillService(repo repositories.SkillsRepository, h SourceHarvester) *SkillService {
	return &SkillService{
		repo:      repo,
		harvester: h,
		labeler: func(ctx context.Context, uri string) (string, error) {
			return commonhttpclient.FetchOrganisationLabel(ctx, uri)
		},
		indexer:    publishSkillToTypesense,
		background: func(f func()) { go f() },
	}
}

// SetBackgroundRunner bepaalt hoe de eerste harvest van een nieuwe bron
// draait; tests draaien hem synchroon.
func (s *SkillService) SetBackgroundRunner(run func(func())) {
	s.background = run
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
	var occurrences []models.SkillOccurrence
	if err == nil {
		// Vóór het ontdubbelen: ook skills die bij een andere bron horen,
		// komen in deze bron voor.
		occurrences = occurrencesOf(source.Id, result.Skills)
		err = s.dropOwnedElsewhere(ctx, source, result)
	}
	if err == nil {
		err = s.repo.ReplaceSourceContent(ctx, source.Id, result.Plugins, result.Skills, occurrences)
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

// dropOwnedElsewhere zorgt dat elke plugin en skill maar één keer in het
// register staat. Ids zijn gebaseerd op repository en pad, dus dezelfde skill
// via twee bronnen (bijvoorbeeld een plugin uit een marketplace die ook los is
// geregistreerd) heeft hetzelfde id. Hij hoort dan bij de meest specifieke bron
// (skill boven plugin boven marketplace); bij gelijk type bij de oudste. Items
// van een andere bron die voorgaat, worden uit het resultaat gehaald; neemt
// deze bron het over, dan verhuist het item met behoud van createdAt.
func (s *SkillService) dropOwnedElsewhere(ctx context.Context, source *models.Source, result *harvester.Result) error {
	pluginIDs := make([]string, len(result.Plugins))
	for i, p := range result.Plugins {
		pluginIDs[i] = p.Id
	}
	skillIDs := make([]string, len(result.Skills))
	for i, sk := range result.Skills {
		skillIDs[i] = sk.Id
	}
	pluginOwners, err := s.repo.PluginOwners(ctx, pluginIDs)
	if err != nil {
		return err
	}
	skillOwners, err := s.repo.SkillOwners(ctx, skillIDs)
	if err != nil {
		return err
	}

	sources := map[string]*models.Source{}
	ownedElsewhere := func(ownerID string) (*models.Source, error) {
		if ownerID == "" || ownerID == source.Id {
			return nil, nil
		}
		other, ok := sources[ownerID]
		if !ok {
			var err error
			if other, err = s.repo.GetSourceByID(ctx, ownerID); err != nil {
				return nil, err
			}
			sources[ownerID] = other
		}
		if other == nil || outranks(source, other) {
			return nil, nil
		}
		return other, nil
	}

	plugins := result.Plugins[:0]
	for _, p := range result.Plugins {
		other, err := ownedElsewhere(pluginOwners[p.Id])
		if err != nil {
			return err
		}
		if other != nil {
			log.Printf("[harvest] %s: plugin %s staat al in bron %s", source.Url, p.Name, other.Url)
			continue
		}
		plugins = append(plugins, p)
	}
	skills := result.Skills[:0]
	for _, sk := range result.Skills {
		other, err := ownedElsewhere(skillOwners[sk.Id])
		if err != nil {
			return err
		}
		if other != nil {
			log.Printf("[harvest] %s: skill %s staat al in bron %s", source.Url, sk.Path, other.Url)
			continue
		}
		skills = append(skills, sk)
	}
	result.Plugins, result.Skills = plugins, skills
	return nil
}

func occurrencesOf(sourceID string, skills []models.Skill) []models.SkillOccurrence {
	out := make([]models.SkillOccurrence, 0, len(skills))
	seen := make(map[string]bool, len(skills))
	for _, sk := range skills {
		if seen[sk.Id] {
			continue
		}
		seen[sk.Id] = true
		out = append(out, models.SkillOccurrence{SkillID: sk.Id, SourceID: sourceID, PluginID: sk.PluginID})
	}
	return out
}

var sourcePriority = map[models.SourceType]int{
	models.SourceTypeMarketplace: 1,
	models.SourceTypePlugin:      2,
	models.SourceTypeSkill:       3,
}

// outranks geeft aan of bron a een item mag hebben dat nu bij b hoort.
func outranks(a, b *models.Source) bool {
	if pa, pb := sourcePriority[a.Type], sourcePriority[b.Type]; pa != pb {
		return pa > pb
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.Id < b.Id
}

// CreateSource registreert een bron, gekoppeld aan een organisatie. De
// organisatie wordt via TOOI aangemaakt als hij nog niet bestaat. De bron wordt
// direct op de achtergrond geharvest en daarna dagelijks door de harvest-job.
func (s *SkillService) CreateSource(ctx context.Context, in models.SourceInput) (*models.Source, error) {
	loc, err := harvester.ParseGitHubLocation(in.Url)
	if err != nil {
		return nil, problem.NewBadRequest("Invalid input", bodyError("url", "invalid", err.Error()))
	}
	url := loc.URL()
	existing, err := s.repo.FindSourceByURL(ctx, url)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, problem.New(http.StatusConflict, "Source already exists",
			bodyError("url", "conflict", "source already exists"))
	}
	org, err := s.ensureOrganisation(ctx, strings.TrimSpace(in.OrganisationUri))
	if err != nil {
		return nil, err
	}

	name := loc.Repo.String()
	if loc.Path != "" {
		name += "/" + loc.Path
	}
	source := &models.Source{
		Id:             uuid.NewString(),
		Type:           in.Type,
		Url:            url,
		Name:           name,
		OrganisationID: &org.Uri,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.repo.SaveSource(ctx, source); err != nil {
		return nil, err
	}
	log.Printf("[sources] bron %s (%s) toegevoegd voor %s", url, in.Type, org.Uri)

	toHarvest := *source
	s.background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), firstHarvestTimeout)
		defer cancel()
		s.HarvestSource(ctx, &toHarvest)
	})

	source.Organisation = org
	return source, nil
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
	plugin := s.Plugin
	if plugin == nil {
		// Een los geregistreerde skill heeft zelf geen plugin; toon dan de
		// plugin waarin hij via een andere bron voorkomt.
		plugin = occurrencePlugin(s.Occurrences)
	}
	if plugin != nil {
		out.Plugin = &models.PluginRef{Id: plugin.Id, Name: plugin.Name, Version: plugin.Version}
	}
	out.Marketplaces = marketplacesOf(s.Occurrences)
	if s.Source != nil {
		out.Source = &models.SourceRef{Id: s.Source.Id, Type: s.Source.Type, Name: s.Source.Name, Url: s.Source.Url}
		if org := s.Source.Organisation; org != nil {
			out.Organisation = &models.OrganisationSummary{Uri: org.Uri, Label: org.Label}
		}
	}
	return out
}

// occurrencePlugin kiest de plugin uit de bron met de hoogste prioriteit.
func occurrencePlugin(occ []models.SkillOccurrence) *models.Plugin {
	var best *models.SkillOccurrence
	for i := range occ {
		o := &occ[i]
		if o.Plugin == nil || o.Source == nil {
			continue
		}
		if best == nil || outranks(o.Source, best.Source) {
			best = o
		}
	}
	if best == nil {
		return nil
	}
	return best.Plugin
}

// marketplacesOf geeft de marketplaces waarin de skill voorkomt, op naam.
func marketplacesOf(occ []models.SkillOccurrence) []models.MarketplaceRef {
	var out []models.MarketplaceRef
	for _, o := range occ {
		if o.Source != nil && o.Source.Type == models.SourceTypeMarketplace {
			out = append(out, models.MarketplaceRef{Id: o.Source.Id, Name: o.Source.Name, Url: o.Source.Url})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Url < out[j].Url
	})
	return out
}

func bodyError(field, code, detail string) problem.ErrorDetail {
	return problem.ErrorDetail{In: "body", Location: "#/" + field, Code: code, Detail: detail}
}
