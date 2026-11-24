/*
 * Skills-register API v1
 *
 * API van het Skills register (skills.developer.overheid.nl)
 *
 * API version: 0.1.0
 * Contact: developer.overheid@geonovum.nl
 */

package models

import (
	"time"

	commonfilters "github.com/developer-overheid-nl/don-register-common/filters"
	commonpagination "github.com/developer-overheid-nl/don-register-common/pagination"
)

// SourceType bepaalt hoe een bron geharvest wordt.
type SourceType string

const (
	// SourceTypeMarketplace is een repository met een
	// .claude-plugin/marketplace.json die naar plugins verwijst.
	SourceTypeMarketplace SourceType = "marketplace"
	// SourceTypePlugin is één plugin: een map of repository met optioneel een
	// .claude-plugin/plugin.json en één of meer SKILL.md bestanden.
	SourceTypePlugin SourceType = "plugin"
	// SourceTypeSkill is één SKILL.md, of alle SKILL.md bestanden onder een
	// map of repository, zonder plugin.
	SourceTypeSkill SourceType = "skill"
)

// Source is een geregistreerde bron die periodiek geharvest wordt.
type Source struct {
	Id              string        `json:"id" gorm:"column:id;primaryKey"`
	Type            SourceType    `json:"type" gorm:"column:type"`
	Url             string        `json:"url" gorm:"column:url;uniqueIndex"`
	Name            string        `json:"name,omitempty" gorm:"column:name"`
	Description     string        `json:"description,omitempty" gorm:"column:description"`
	Organisation    *Organisation `json:"organisation,omitempty" gorm:"foreignKey:OrganisationID;references:Uri"`
	OrganisationID  *string       `json:"-" gorm:"column:organisation_id"`
	LastHarvestedAt *time.Time    `json:"lastHarvestedAt,omitempty" gorm:"column:last_harvested_at"`
	LastError       string        `json:"lastError,omitempty" gorm:"column:last_error"`
	CreatedAt       time.Time     `json:"createdAt" gorm:"column:created_at"`
}

// Plugin is een plugin uit een marketplace: een bundel van skills.
type Plugin struct {
	Id          string    `json:"id" gorm:"column:id;primaryKey"`
	SourceID    string    `json:"sourceId" gorm:"column:source_id;index"`
	Name        string    `json:"name" gorm:"column:name"`
	DisplayName string    `json:"displayName,omitempty" gorm:"column:display_name"`
	Description string    `json:"description,omitempty" gorm:"column:description"`
	Version     string    `json:"version,omitempty" gorm:"column:version"`
	Author      string    `json:"author,omitempty" gorm:"column:author"`
	RepoUrl     string    `json:"repoUrl,omitempty" gorm:"column:repo_url"`
	Category    string    `json:"category,omitempty" gorm:"column:category"`
	Tags        []string  `json:"tags,omitempty" gorm:"column:tags;serializer:json"`
	CreatedAt   time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

// Skill is één SKILL.md, gevonden in een plugin of in een losse repository.
type Skill struct {
	Id       string  `json:"id" gorm:"column:id;primaryKey"`
	SourceID string  `json:"sourceId" gorm:"column:source_id;index"`
	Source   *Source `json:"-" gorm:"foreignKey:SourceID;references:Id"`
	PluginID *string `json:"pluginId,omitempty" gorm:"column:plugin_id;index"`
	Plugin   *Plugin `json:"-" gorm:"foreignKey:PluginID;references:Id"`
	// Occurrences zijn alle bronnen waarin de skill voorkomt, ook bronnen die
	// hem niet bezitten. Zo blijven plugin en marketplaces zichtbaar.
	Occurrences []SkillOccurrence `json:"-" gorm:"foreignKey:SkillID;references:Id;constraint:-"`
	Name        string            `json:"name" gorm:"column:name"`
	Description string            `json:"description,omitempty" gorm:"column:description"`
	RepoUrl     string            `json:"repoUrl" gorm:"column:repo_url"`
	Path        string            `json:"path" gorm:"column:path"`
	Ref         string            `json:"ref,omitempty" gorm:"column:ref"`
	Frontmatter map[string]any    `json:"frontmatter,omitempty" gorm:"column:frontmatter;serializer:json"`
	Content     string            `json:"-" gorm:"column:content"`
	Hash        string            `json:"-" gorm:"column:content_hash"`
	CreatedAt   time.Time         `json:"createdAt" gorm:"column:created_at"`
	LastCrawled time.Time         `json:"lastCrawledAt" gorm:"column:last_crawled_at"`
}

// SkillOccurrence legt vast dat een skill bij de laatste harvest van een bron
// gevonden werd, en in welke plugin. Een skill staat maar één keer in het
// register (bij één bron), maar kan via meerdere bronnen voorkomen.
type SkillOccurrence struct {
	SkillID  string  `gorm:"column:skill_id;primaryKey"`
	SourceID string  `gorm:"column:source_id;primaryKey;index"`
	Source   *Source `gorm:"foreignKey:SourceID;references:Id;constraint:OnDelete:CASCADE"`
	PluginID *string `gorm:"column:plugin_id"`
	// Geen foreign keys naar skills en plugins: die kunnen bij hun eigen bron
	// verdwijnen terwijl een andere bron ze nog noemt.
	Plugin *Plugin `gorm:"foreignKey:PluginID;references:Id;constraint:-"`
}

// SkillSummary is de lijstweergave van een skill.
type SkillSummary struct {
	Id            string               `json:"id"`
	Name          string               `json:"name"`
	Description   string               `json:"description,omitempty"`
	RepoUrl       string               `json:"repoUrl"`
	Path          string               `json:"path"`
	Plugin        *PluginRef           `json:"plugin,omitempty"`
	Marketplaces  []MarketplaceRef     `json:"marketplaces,omitempty"`
	Source        *SourceRef           `json:"source,omitempty"`
	Organisation  *OrganisationSummary `json:"organisation,omitempty"`
	CreatedAt     time.Time            `json:"createdAt"`
	LastCrawledAt time.Time            `json:"lastCrawledAt"`
}

// SkillDetail bevat daarnaast de frontmatter en de inhoud van SKILL.md.
type SkillDetail struct {
	SkillSummary
	Ref         string         `json:"ref,omitempty"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Content     string         `json:"content"`
}

type PluginRef struct {
	Id      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type SourceRef struct {
	Id   string     `json:"id"`
	Type SourceType `json:"type"`
	Name string     `json:"name,omitempty"`
	Url  string     `json:"url"`
}

// MarketplaceRef is een marketplace-bron waarin de skill voorkomt.
type MarketplaceRef struct {
	Id   string `json:"id"`
	Name string `json:"name,omitempty"`
	Url  string `json:"url"`
}

// SourceInput registreert een bron. Harvesten gebeurt door de harvest-job.
type SourceInput struct {
	Type            SourceType `json:"type" binding:"required,oneof=marketplace plugin skill"`
	Url             string     `json:"url" binding:"required,url"`
	OrganisationUri string     `json:"organisationUri" binding:"required,url"`
}

type IdParams struct {
	Id string `path:"id"`
}

type ListParams struct {
	Page    int `query:"page" validate:"omitempty,min=1"`
	PerPage int `query:"perPage" validate:"omitempty,min=1,max=100"`
}

type ListSkillsParams struct {
	Page    int `query:"page" validate:"omitempty,min=1"`
	PerPage int `query:"perPage" validate:"omitempty,min=1,max=100"`
	SkillFiltersParams
}

type SkillFiltersParams struct {
	Query        string   `query:"q"`
	Plugin       []string `query:"plugin"`
	Source       []string `query:"source"`
	Organisation []string `query:"organisation"`
}

type Organisation struct {
	Uri   string `gorm:"column:uri;primaryKey" json:"uri" binding:"required,url"`
	Label string `gorm:"column:label" json:"label"`
}

type OrganisationSummary struct {
	Uri   string `json:"uri"`
	Label string `json:"label"`
}

// Pagination en filters worden gedeeld via don-register-common.
type Pagination = commonpagination.Pagination

type FilterOption = commonfilters.FilterOption

type FilterGroup = commonfilters.FilterGroup

type FilterCount = commonfilters.FilterCount
