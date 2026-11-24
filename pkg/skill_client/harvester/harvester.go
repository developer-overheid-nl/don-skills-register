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
	pluginManifest      = ".claude-plugin/plugin.json"
	skillFileName       = "SKILL.md"
)

// idNamespace zorgt voor stabiele ids op basis van repository en pad: dezelfde
// skill krijgt bij elke harvest, en via elke bron, hetzelfde id. Zo blijven
// links en zoekindex kloppen en staat een skill maar één keer in het register.
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
	loc, err := ParseGitHubLocation(source.Url)
	if err != nil {
		return nil, err
	}
	switch source.Type {
	case models.SourceTypeMarketplace:
		return h.harvestMarketplace(ctx, source, loc)
	case models.SourceTypePlugin:
		return h.harvestPlugin(ctx, source, loc)
	case models.SourceTypeSkill:
		return h.harvestSkills(ctx, source, loc)
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

// pluginFile volgt het formaat van .claude-plugin/plugin.json.
type pluginFile struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Category    string          `json:"category"`
	Keywords    []string        `json:"keywords"`
	Author      json.RawMessage `json:"author"`
}

// pluginLocation is waar de bestanden van een plugin staan.
type pluginLocation struct {
	repo RepoRef
	ref  string
	dir  string
	// file beperkt de harvest tot één SKILL.md.
	file string
}

func (h *Harvester) harvestMarketplace(ctx context.Context, source models.Source, loc Location) (*Result, error) {
	repo := loc.Repo
	sha, err := h.repos.ResolveCommit(ctx, repo, loc.Ref)
	if err != nil {
		return nil, err
	}
	manifestPath := path.Join(loc.Path, marketplaceManifest)
	raw, err := h.repos.ReadFile(ctx, repo, sha, manifestPath)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%s bevat geen %s", loc.URL(), marketplaceManifest)
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
		pluginLoc, err := resolvePluginSource(mp.Source, repo, sha, loc.Path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("plugin %q: %v", mp.Name, err))
			continue
		}
		plugin := models.Plugin{
			Id:          stableID("plugin", pluginLoc.repo.String(), pluginLoc.dir),
			SourceID:    source.Id,
			Name:        mp.Name,
			DisplayName: mp.DisplayName,
			Description: mp.Description,
			Version:     mp.Version,
			Author:      authorName(mp.Author),
			RepoUrl:     pluginLoc.repo.URL(),
			Category:    mp.Category,
			Tags:        mergeTags(mp.Tags, mp.Keywords),
		}
		skills, warnings, err := h.collectSkills(ctx, source, &plugin.Id, pluginLoc)
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

// harvestPlugin harvest één plugin: een map (of repository) met optioneel een
// .claude-plugin/plugin.json en de SKILL.md bestanden daaronder.
func (h *Harvester) harvestPlugin(ctx context.Context, source models.Source, loc Location) (*Result, error) {
	sha, err := h.repos.ResolveCommit(ctx, loc.Repo, loc.Ref)
	if err != nil {
		return nil, err
	}
	var manifest pluginFile
	raw, err := h.repos.ReadFile(ctx, loc.Repo, sha, path.Join(loc.Path, pluginManifest))
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, fmt.Errorf("%s: ongeldige JSON: %w", pluginManifest, err)
		}
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}

	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		// Zonder plugin.json leidt Claude Code de naam af van de map.
		name = loc.Repo.Repo
		if loc.Path != "" {
			name = path.Base(loc.Path)
		}
	}
	plugin := models.Plugin{
		Id:          stableID("plugin", loc.Repo.String(), loc.Path),
		SourceID:    source.Id,
		Name:        name,
		DisplayName: manifest.DisplayName,
		Description: manifest.Description,
		Version:     manifest.Version,
		Author:      authorName(manifest.Author),
		RepoUrl:     loc.Repo.URL(),
		Category:    manifest.Category,
		Tags:        mergeTags(manifest.Keywords),
	}
	skills, warnings, err := h.collectSkills(ctx, source, &plugin.Id, pluginLocation{repo: loc.Repo, ref: sha, dir: loc.Path})
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, noSkillsError(loc, warnings)
	}
	return &Result{
		Name:        name,
		Description: manifest.Description,
		Plugins:     []models.Plugin{plugin},
		Skills:      skills,
		Warnings:    warnings,
	}, nil
}

// harvestSkills harvest losse skills zonder plugin: één SKILL.md (een
// …/blob/… URL), of alle SKILL.md bestanden onder een map of repository.
func (h *Harvester) harvestSkills(ctx context.Context, source models.Source, loc Location) (*Result, error) {
	sha, err := h.repos.ResolveCommit(ctx, loc.Repo, loc.Ref)
	if err != nil {
		return nil, err
	}
	target := pluginLocation{repo: loc.Repo, ref: sha, dir: loc.Path}
	if path.Base(loc.Path) == skillFileName {
		target.dir = path.Dir(loc.Path)
		target.file = loc.Path
	}
	skills, warnings, err := h.collectSkills(ctx, source, nil, target)
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, noSkillsError(loc, warnings)
	}
	name := loc.Repo.String()
	if len(skills) == 1 {
		name = skills[0].Name
	} else if loc.Path != "" {
		name += "/" + loc.Path
	}
	return &Result{Name: name, Skills: skills, Warnings: warnings}, nil
}

func noSkillsError(loc Location, warnings []string) error {
	if len(warnings) > 0 {
		return fmt.Errorf("geen geldige SKILL.md gevonden in %s: %s", loc.URL(), strings.Join(warnings, "; "))
	}
	return fmt.Errorf("geen SKILL.md gevonden in %s", loc.URL())
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
	if prefix == "." {
		prefix = ""
	}
	if prefix != "" {
		prefix += "/"
	}
	var paths []string
	for _, p := range files {
		if loc.file != "" && p != loc.file {
			continue
		}
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
			Id:          stableID("skill", loc.repo.String(), p),
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
// naar een locatie. Ondersteund: een relatief pad vanaf de marketplace-root
// (baseDir, de map met .claude-plugin/),
// {"source":"github"}, en {"source":"url"} / {"source":"git-subdir"} naar
// github.com. npm en andere hosts worden (nog) overgeslagen.
func resolvePluginSource(raw json.RawMessage, marketplace RepoRef, marketplaceSHA, baseDir string) (pluginLocation, error) {
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
		dir := path.Join(baseDir, clean)
		if dir == "." {
			dir = ""
		}
		return pluginLocation{repo: marketplace, ref: marketplaceSHA, dir: dir}, nil
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
