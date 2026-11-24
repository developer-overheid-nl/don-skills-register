package repositories

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	commonpagination "github.com/developer-overheid-nl/don-register-common/pagination"
	commonquery "github.com/developer-overheid-nl/don-register-common/query"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SkillsRepository interface {
	ListSkills(ctx context.Context, page, perPage int, p *models.SkillFiltersParams) ([]models.Skill, models.Pagination, error)
	GetSkillByID(ctx context.Context, id string) (*models.Skill, error)
	GetSkillFilterCounts(ctx context.Context, p *models.SkillFiltersParams) (*SkillFilterCounts, error)

	ListSources(ctx context.Context, page, perPage int) ([]models.Source, models.Pagination, error)
	AllSources(ctx context.Context) ([]models.Source, error)
	GetSourceByID(ctx context.Context, id string) (*models.Source, error)
	FindSourceByURL(ctx context.Context, url string) (*models.Source, error)
	SaveSource(ctx context.Context, source *models.Source) error
	// PluginOwners en SkillOwners geven per id de bron die het item nu heeft.
	PluginOwners(ctx context.Context, ids []string) (map[string]string, error)
	SkillOwners(ctx context.Context, ids []string) (map[string]string, error)
	// ReplaceSourceContent vervangt alle plugins en skills van een bron door
	// het resultaat van de laatste harvest. Verdwenen items worden verwijderd.
	// occurrences vervangt de skills die in de bron voorkomen, ook die van een
	// andere bron zijn.
	ReplaceSourceContent(ctx context.Context, sourceID string, plugins []models.Plugin, skills []models.Skill, occurrences []models.SkillOccurrence) error

	ListOrganisations(ctx context.Context, page, perPage int) ([]models.Organisation, models.Pagination, error)
	FindOrganisationByURI(ctx context.Context, uri string) (*models.Organisation, error)
	SaveOrganisation(ctx context.Context, org *models.Organisation) error
}

type SkillFilterCounts struct {
	Plugin       []models.FilterCount
	Source       []models.FilterCount
	Organisation []models.FilterCount
}

type skillsRepository struct {
	db *gorm.DB
}

func NewSkillsRepository(db *gorm.DB) SkillsRepository {
	return &skillsRepository{db: db}
}

func (r *skillsRepository) preloadedSkills(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).
		Preload("Plugin").
		Preload("Source").
		Preload("Source.Organisation").
		Preload("Occurrences.Source").
		Preload("Occurrences.Plugin")
}

// ListSkills filtert in-memory, net als het schema-register. Het register
// bevat honderden, geen miljoenen skills.
func (r *skillsRepository) ListSkills(ctx context.Context, page, perPage int, p *models.SkillFiltersParams) ([]models.Skill, models.Pagination, error) {
	page, perPage = commonpagination.Normalize(page, perPage, 20, 100)
	var all []models.Skill
	if err := r.preloadedSkills(ctx).Order("name").Order("id").Find(&all).Error; err != nil {
		return nil, models.Pagination{}, err
	}
	filtered := make([]models.Skill, 0, len(all))
	for _, s := range all {
		if skillMatches(s, p, "") {
			filtered = append(filtered, s)
		}
	}
	pagination := commonpagination.New(page, perPage, len(filtered))
	offset := (page - 1) * perPage
	if offset >= len(filtered) {
		return []models.Skill{}, pagination, nil
	}
	end := min(offset+perPage, len(filtered))
	return filtered[offset:end], pagination, nil
}

func (r *skillsRepository) GetSkillByID(ctx context.Context, id string) (*models.Skill, error) {
	var skill models.Skill
	if err := r.preloadedSkills(ctx).First(&skill, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &skill, nil
}

func (r *skillsRepository) GetSkillFilterCounts(ctx context.Context, p *models.SkillFiltersParams) (*SkillFilterCounts, error) {
	var all []models.Skill
	if err := r.preloadedSkills(ctx).Find(&all).Error; err != nil {
		return nil, err
	}
	return &SkillFilterCounts{
		Plugin: commonquery.CountByFieldWithLabel(all,
			func(s models.Skill) bool { return skillMatches(s, p, "plugin") && s.PluginID != nil },
			func(s models.Skill) string { return derefOr(s.PluginID) },
			func(s models.Skill) string {
				if s.Plugin == nil {
					return ""
				}
				if s.Plugin.DisplayName != "" {
					return s.Plugin.DisplayName
				}
				return s.Plugin.Name
			},
		),
		Source: commonquery.CountByFieldWithLabel(all,
			func(s models.Skill) bool { return skillMatches(s, p, "source") },
			func(s models.Skill) string { return s.SourceID },
			func(s models.Skill) string {
				if s.Source == nil {
					return ""
				}
				if s.Source.Name != "" {
					return s.Source.Name
				}
				return s.Source.Url
			},
		),
		Organisation: commonquery.CountByFieldWithLabel(all,
			func(s models.Skill) bool { return skillMatches(s, p, "organisation") && skillOrganisation(s) != nil },
			func(s models.Skill) string { return skillOrganisation(s).Uri },
			func(s models.Skill) string { return skillOrganisation(s).Label },
		),
	}, nil
}

func (r *skillsRepository) ListSources(ctx context.Context, page, perPage int) ([]models.Source, models.Pagination, error) {
	return paginate[models.Source](r.db.WithContext(ctx).Preload("Organisation").Order("url"), page, perPage)
}

func (r *skillsRepository) AllSources(ctx context.Context) ([]models.Source, error) {
	var sources []models.Source
	err := r.db.WithContext(ctx).Preload("Organisation").Order("url").Find(&sources).Error
	return sources, err
}

func (r *skillsRepository) GetSourceByID(ctx context.Context, id string) (*models.Source, error) {
	return first[models.Source](r.db.WithContext(ctx).Preload("Organisation"), id)
}

func (r *skillsRepository) FindSourceByURL(ctx context.Context, url string) (*models.Source, error) {
	var source models.Source
	if err := r.db.WithContext(ctx).Where("url = ?", url).First(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &source, nil
}

func (r *skillsRepository) SaveSource(ctx context.Context, source *models.Source) error {
	return r.db.WithContext(ctx).Omit("Organisation").Save(source).Error
}

func (r *skillsRepository) PluginOwners(ctx context.Context, ids []string) (map[string]string, error) {
	return owners[models.Plugin](r.db.WithContext(ctx), ids)
}

func (r *skillsRepository) SkillOwners(ctx context.Context, ids []string) (map[string]string, error) {
	return owners[models.Skill](r.db.WithContext(ctx), ids)
}

func (r *skillsRepository) ReplaceSourceContent(ctx context.Context, sourceID string, plugins []models.Plugin, skills []models.Skill, occurrences []models.SkillOccurrence) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		createdAt, err := existingCreatedAt[models.Skill](tx, ids(skills, func(s models.Skill) string { return s.Id }))
		if err != nil {
			return err
		}
		pluginCreatedAt, err := existingCreatedAt[models.Plugin](tx, ids(plugins, func(p models.Plugin) string { return p.Id }))
		if err != nil {
			return err
		}
		now := time.Now().UTC()

		skillIDs := make([]string, 0, len(skills))
		for i := range skills {
			skillIDs = append(skillIDs, skills[i].Id)
			skills[i].CreatedAt = createdAtOr(createdAt, skills[i].Id, now)
		}
		pluginIDs := make([]string, 0, len(plugins))
		for i := range plugins {
			pluginIDs = append(pluginIDs, plugins[i].Id)
			plugins[i].CreatedAt = createdAtOr(pluginCreatedAt, plugins[i].Id, now)
			plugins[i].UpdatedAt = now
		}

		if err := deleteMissing[models.Skill](tx, sourceID, skillIDs); err != nil {
			return err
		}
		if err := deleteMissing[models.Plugin](tx, sourceID, pluginIDs); err != nil {
			return err
		}
		if len(plugins) > 0 {
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&plugins).Error; err != nil {
				return err
			}
		}
		if len(skills) > 0 {
			if err := tx.Omit("Source", "Plugin", "Occurrences").Clauses(clause.OnConflict{UpdateAll: true}).Create(&skills).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("source_id = ?", sourceID).Delete(&models.SkillOccurrence{}).Error; err != nil {
			return err
		}
		if len(occurrences) > 0 {
			if err := tx.Omit("Source", "Plugin").Create(&occurrences).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *skillsRepository) ListOrganisations(ctx context.Context, page, perPage int) ([]models.Organisation, models.Pagination, error) {
	return paginate[models.Organisation](r.db.WithContext(ctx).Order("label asc"), page, perPage)
}

func (r *skillsRepository) FindOrganisationByURI(ctx context.Context, uri string) (*models.Organisation, error) {
	var org models.Organisation
	if err := r.db.WithContext(ctx).Where("uri = ?", uri).First(&org).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &org, nil
}

func (r *skillsRepository) SaveOrganisation(ctx context.Context, org *models.Organisation) error {
	return r.db.WithContext(ctx).Save(org).Error
}

func paginate[T any](db *gorm.DB, page, perPage int) ([]T, models.Pagination, error) {
	page, perPage = commonpagination.Normalize(page, perPage, 20, 100)
	var total int64
	if err := db.Session(&gorm.Session{}).Model(new(T)).Count(&total).Error; err != nil {
		return nil, models.Pagination{}, err
	}
	var items []T
	if err := db.Offset((page - 1) * perPage).Limit(perPage).Find(&items).Error; err != nil {
		return nil, models.Pagination{}, err
	}
	return items, commonpagination.New(page, perPage, int(total)), nil
}

func first[T any](db *gorm.DB, id string) (*T, error) {
	var item T
	if err := db.First(&item, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

type idWithCreatedAt struct {
	Id        string
	CreatedAt time.Time
}

// existingCreatedAt zoekt de createdAt van bestaande items op id, ook als een
// andere bron ze eerder had.
func existingCreatedAt[T any](tx *gorm.DB, ids []string) (map[string]time.Time, error) {
	if len(ids) == 0 {
		return map[string]time.Time{}, nil
	}
	var rows []idWithCreatedAt
	if err := tx.Model(new(T)).Select("id", "created_at").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		out[row.Id] = row.CreatedAt
	}
	return out, nil
}

type idWithSource struct {
	Id       string
	SourceID string
}

func owners[T any](db *gorm.DB, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []idWithSource
	if err := db.Model(new(T)).Select("id", "source_id").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.Id] = row.SourceID
	}
	return out, nil
}

func ids[T any](items []T, id func(T) string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = id(item)
	}
	return out
}

func createdAtOr(existing map[string]time.Time, id string, fallback time.Time) time.Time {
	if t, ok := existing[id]; ok && !t.IsZero() {
		return t
	}
	return fallback
}

func deleteMissing[T any](tx *gorm.DB, sourceID string, keep []string) error {
	q := tx.Where("source_id = ?", sourceID)
	if len(keep) > 0 {
		q = q.Where("id NOT IN ?", keep)
	}
	return q.Delete(new(T)).Error
}

func skillOrganisation(s models.Skill) *models.Organisation {
	if s.Source == nil {
		return nil
	}
	return s.Source.Organisation
}

func skillMatches(s models.Skill, p *models.SkillFiltersParams, exclude string) bool {
	if p == nil {
		return true
	}
	if q := strings.ToLower(strings.TrimSpace(p.Query)); q != "" {
		pluginMatch := s.Plugin != nil && strings.Contains(strings.ToLower(s.Plugin.Name), q)
		if !pluginMatch &&
			!strings.Contains(strings.ToLower(s.Name), q) &&
			!strings.Contains(strings.ToLower(s.Description), q) {
			return false
		}
	}
	if exclude != "plugin" && len(p.Plugin) > 0 && !slices.Contains(p.Plugin, derefOr(s.PluginID)) {
		return false
	}
	if exclude != "source" && len(p.Source) > 0 && !slices.Contains(p.Source, s.SourceID) {
		return false
	}
	if exclude != "organisation" && len(p.Organisation) > 0 {
		org := skillOrganisation(s)
		if org == nil || !slices.Contains(p.Organisation, org.Uri) {
			return false
		}
	}
	return true
}

func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
