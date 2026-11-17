package harvester

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"github.com/google/uuid"
)

const (
	marketplaceManifest = ".claude-plugin/marketplace.json"
	skillFileName       = "SKILL.md"
)

// idNamespace zorgt voor stabiele ids: dezelfde skill krijgt bij elke harvest
// hetzelfde id, zodat links en zoekindex blijven kloppen.
var idNamespace = uuid.MustParse("6f1d3c64-4a39-4f0e-9a0a-2f1b9d3a7c11")

// Repository is wat de harvester van een git-host nodig heeft.
type Repository interface {
	ResolveCommit(ctx context.Context, repo RepoRef, ref string) (string, error)
	ListFiles(ctx context.Context, repo RepoRef, sha string) ([]string, error)
	ReadFile(ctx context.Context, repo RepoRef, sha, path string) ([]byte, error)
}

// Result is de uitkomst van het harvesten van één bron.
type Result struct {
	Name        string
	Description string
	Plugins     []models.Plugin
	Skills      []models.Skill
	// Warnings bevat problemen met individuele plugins of skills die de
	// harvest van de rest niet blokkeren.
	Warnings []string
}

type Harvester struct {
	repos Repository
	now   func() time.Time
}

func New(repos Repository) *Harvester {
	return &Harvester{repos: repos, now: time.Now}
}

// Harvest haalt alle plugins en skills op voor een bron.
func (h *Harvester) Harvest(ctx context.Context, source models.Source) (*Result, error) {
	repo, err := ParseGitHubURL(source.Url)
	if err != nil {
		return nil, err
	}
	switch source.Type {
	case models.SourceTypeMarketplace:
		return h.harvestMarketplace(ctx, source, repo)
	case models.SourceTypeRepository:
		return h.harvestRepository(ctx, source, repo)
	default:
		return nil, fmt.Errorf("onbekend brontype %q", source.Type)
	}
}

// marketplaceFile volgt het formaat van .claude-plugin/marketplace.json.
type marketplaceFile struct {
	Name     string `json:"name"`
	Metadata struct {
		Description string `json:"description"`
	} `json:"metadata"`
	Plugins []marketplacePlugin `json:"plugins"`
}

type marketplacePlugin struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Category    string          `json:"category"`
	Tags        []string        `json:"tags"`
	Keywords    []string        `json:"keywords"`
	Author      json.RawMessage `json:"author"`
	Source      json.RawMessage `json:"source"`
}

// pluginLocation is waar de bestanden van een plugin staan.
type pluginLocation struct {
	repo RepoRef
	ref  string
	dir  string
}

func (h *Harvester) harvestMarketplace(ctx context.Context, source models.Source, repo RepoRef) (*Result, error) {
	sha, err := h.repos.ResolveCommit(ctx, repo, "")
	if err != nil {
		return nil, err
	}
	raw, err := h.repos.ReadFile(ctx, repo, sha, marketplaceManifest)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%s bevat geen %s", repo, marketplaceManifest)
		}
		return nil, err
	}
	var manifest marketplaceFile
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%s: ongeldige JSON: %w", marketplaceManifest, err)
	}

	result := &Result{Name: manifest.Name, Description: manifest.Metadata.Description}
	for _, mp := range manifest.Plugins {
		if strings.TrimSpace(mp.Name) == "" {
			result.Warnings = append(result.Warnings, "plugin zonder naam overgeslagen")
			continue
		}
		loc, err := resolvePluginSource(mp.Source, repo, sha)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("plugin %q: %v", mp.Name, err))
			continue
		}
		plugin := models.Plugin{
			Id:          stableID("plugin", source.Id, mp.Name),
			SourceID:    source.Id,
			Name:        mp.Name,
			DisplayName: mp.DisplayName,
			Description: mp.Description,
			Version:     mp.Version,
			Author:      authorName(mp.Author),
			RepoUrl:     loc.repo.URL(),
			Category:    mp.Category,
			Tags:        mergeTags(mp.Tags, mp.Keywords),
		}
		skills, warnings, err := h.collectSkills(ctx, source, &plugin.Id, loc)
		result.Warnings = append(result.Warnings, prefixAll("plugin "+mp.Name+": ", warnings)...)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("plugin %q: %v", mp.Name, err))
			continue
		}
		result.Plugins = append(result.Plugins, plugin)
		result.Skills = append(result.Skills, skills...)
	}
	return result, nil
}

func (h *Harvester) harvestRepository(ctx context.Context, source models.Source, repo RepoRef) (*Result, error) {
	skills, warnings, err := h.collectSkills(ctx, source, nil, pluginLocation{repo: repo})
	if err != nil {
		return nil, err
	}
	return &Result{Name: repo.String(), Skills: skills, Warnings: warnings}, nil
}

// collectSkills zoekt alle SKILL.md bestanden onder loc.dir.
func (h *Harvester) collectSkills(ctx context.Context, source models.Source, pluginID *string, loc pluginLocation) ([]models.Skill, []string, error) {
	sha := loc.ref
	if sha == "" || !looksLikeSHA(sha) {
		resolved, err := h.repos.ResolveCommit(ctx, loc.repo, loc.ref)
		if err != nil {
			return nil, nil, err
		}
		sha = resolved
	}
	files, err := h.repos.ListFiles(ctx, loc.repo, sha)
	if err != nil {
		return nil, nil, err
	}

	prefix := strings.Trim(loc.dir, "/")
	if prefix != "" {
		prefix += "/"
	}
	var paths []string
	for _, p := range files {
		if path.Base(p) == skillFileName && strings.HasPrefix(p, prefix) && !strings.Contains(p, "node_modules/") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	now := h.now().UTC()
	var skills []models.Skill
	var warnings []string
	for _, p := range paths {
		content, err := h.repos.ReadFile(ctx, loc.repo, sha, p)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		doc, err := ParseSkillMarkdown(content)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		skills = append(skills, models.Skill{
			Id:          stableID("skill", source.Id, loc.repo.String(), p),
			SourceID:    source.Id,
			PluginID:    pluginID,
			Name:        doc.Name,
			Description: doc.Description,
			RepoUrl:     loc.repo.URL(),
			Path:        p,
			Ref:         sha,
			Frontmatter: doc.Frontmatter,
			Content:     string(content),
			Hash:        contentHash(content),
			LastCrawled: now,
		})
	}
	return skills, warnings, nil
}

// resolvePluginSource vertaalt het `source` veld van een marketplace-plugin
// naar een locatie. Ondersteund: een relatief pad binnen de marketplace-repo,
// {"source":"github"}, en {"source":"url"} / {"source":"git-subdir"} naar
// github.com. npm en andere hosts worden (nog) overgeslagen.
func resolvePluginSource(raw json.RawMessage, marketplace RepoRef, marketplaceSHA string) (pluginLocation, error) {
	var rel string
	if err := json.Unmarshal(raw, &rel); err == nil {
		rel = strings.TrimSpace(rel)
		if !strings.HasPrefix(rel, "./") && rel != "." {
			return pluginLocation{}, fmt.Errorf("relatief pad moet met ./ beginnen: %q", rel)
		}
		clean := path.Clean(rel)
		if strings.HasPrefix(clean, "..") {
			return pluginLocation{}, fmt.Errorf("pad buiten de repository: %q", rel)
		}
		if clean == "." {
			clean = ""
		}
		return pluginLocation{repo: marketplace, ref: marketplaceSHA, dir: clean}, nil
	}

	var obj struct {
		Source string `json:"source"`
		Repo   string `json:"repo"`
		URL    string `json:"url"`
		Path   string `json:"path"`
		Ref    string `json:"ref"`
		SHA    string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return pluginLocation{}, fmt.Errorf("onbekend source-formaat")
	}
	ref := obj.SHA
	if ref == "" {
		ref = obj.Ref
	}
	switch obj.Source {
	case "github":
		repo, err := ParseGitHubURL(obj.Repo)
		if err != nil {
			return pluginLocation{}, err
		}
		return pluginLocation{repo: repo, ref: ref}, nil
	case "url", "git-subdir":
		repo, err := ParseGitHubURL(obj.URL)
		if err != nil {
			return pluginLocation{}, err
		}
		return pluginLocation{repo: repo, ref: ref, dir: obj.Path}, nil
	default:
		return pluginLocation{}, fmt.Errorf("source-type %q wordt nog niet ondersteund", obj.Source)
	}
}

func authorName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		log.Printf("[harvester] onbekend author-formaat: %s", string(raw))
		return ""
	}
	return obj.Name
}

func mergeTags(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, g := range groups {
		for _, t := range g {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

func prefixAll(prefix string, values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = prefix + v
	}
	return out
}

func stableID(parts ...string) string {
	return uuid.NewSHA1(idNamespace, []byte(strings.Join(parts, "\x00"))).String()
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func looksLikeSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, r := range ref {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
