package harvester

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SkillDocument is een geparste SKILL.md.
type SkillDocument struct {
	Name        string
	Description string
	Frontmatter map[string]any
	Body        string
}

// ParseSkillMarkdown splitst een SKILL.md in YAML-frontmatter en body. Volgens
// de Agent Skills-specificatie zijn `name` en `description` verplicht.
func ParseSkillMarkdown(content []byte) (SkillDocument, error) {
	text := strings.ReplaceAll(string(bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return SkillDocument{}, fmt.Errorf("SKILL.md mist YAML-frontmatter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return SkillDocument{}, fmt.Errorf("SKILL.md frontmatter is niet afgesloten")
	}
	rawYAML := rest[:end]
	body := strings.TrimPrefix(rest[end+len("\n---"):], "\n")

	fm := map[string]any{}
	if err := yaml.Unmarshal([]byte(rawYAML), &fm); err != nil {
		return SkillDocument{}, fmt.Errorf("SKILL.md frontmatter is geen geldige YAML: %w", err)
	}

	doc := SkillDocument{
		Name:        strings.TrimSpace(stringValue(fm["name"])),
		Description: strings.TrimSpace(stringValue(fm["description"])),
		Frontmatter: fm,
		Body:        body,
	}
	if doc.Name == "" {
		return SkillDocument{}, fmt.Errorf("SKILL.md frontmatter mist 'name'")
	}
	if doc.Description == "" {
		return SkillDocument{}, fmt.Errorf("SKILL.md frontmatter mist 'description'")
	}
	return doc, nil
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
