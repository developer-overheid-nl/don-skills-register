package services

import (
	"context"
	"strings"

	commonhttpclient "github.com/developer-overheid-nl/don-register-common/httpclient"
	commontypesense "github.com/developer-overheid-nl/don-register-common/typesense"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
)

const (
	defaultTypesenseCollection    = "skills-register"
	defaultTypesenseDetailBaseURL = "https://skills.developer.overheid.nl/skills"
)

func loadTypesenseConfig() commontypesense.Config {
	return commontypesense.LoadConfigFromEnv(commontypesense.Defaults{
		Collection:    defaultTypesenseCollection,
		DetailBaseURL: defaultTypesenseDetailBaseURL,
		Language:      "nl",
		ItemPriority:  1,
		DefaultTags:   []string{"skills-register", "skill"},
	})
}

func publishSkillToTypesense(ctx context.Context, skill models.Skill) error {
	cfg := loadTypesenseConfig()
	if !cfg.Enabled() {
		return commontypesense.ErrDisabled
	}
	return commontypesense.UpsertDocument(ctx, commonhttpclient.HTTPClient, cfg, buildTypesenseDocument(cfg, skill))
}

func buildTypesenseDocument(cfg commontypesense.Config, skill models.Skill) map[string]any {
	doc := commontypesense.BaseDocument(cfg, skill.Id)
	doc["hierarchy.lvl0"] = skill.Name
	if skill.Plugin != nil {
		doc["hierarchy.lvl1"] = skill.Plugin.Name
	}
	doc["hierarchy.lvl2"] = "AI skill"
	if d := strings.TrimSpace(skill.Description); d != "" {
		doc["content"] = d
	}
	seen := map[string]struct{}{}
	var tags []string
	for _, t := range cfg.DefaultTags {
		tags = commontypesense.AppendUnique(tags, t, seen)
	}
	if skill.Plugin != nil {
		for _, t := range skill.Plugin.Tags {
			tags = commontypesense.AppendUnique(tags, t, seen)
		}
	}
	if len(tags) > 0 {
		doc["tags"] = tags
	}
	return doc
}
