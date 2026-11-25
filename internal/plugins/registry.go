package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	validCats     = map[string]bool{
		"latency":   true,
		"journal":   true,
		"alerts":    true,
		"host":      true,
		"container": true,
	}
	validSeverities = map[string]bool{
		"info":     true,
		"warn":     true,
		"critical": true,
	}
)

// Rule defines an automated condition and action attached to a community plugin preset.
type Rule struct {
	ID       string `json:"id"`
	Match    string `json:"match"`
	Severity string `json:"severity"`
	Action   string `json:"action"`
}

// Plugin represents a validated tradebox-local community plugin manifest.
type Plugin struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Version            string         `json:"version"`
	Category           string         `json:"category"`
	Author             string         `json:"author"`
	Description        string         `json:"description"`
	MinTradeBoxVersion string         `json:"min_tradebox_version"`
	EnabledByDefault   bool           `json:"enabled_by_default"`
	Tags               []string       `json:"tags"`
	Triggers           []string       `json:"triggers"`
	Config             map[string]any `json:"config"`
	Rules              []Rule         `json:"rules"`
	File               string         `json:"file,omitempty"`
}

// FileResult reports validation results for a single plugin manifest file.
type FileResult struct {
	File   string   `json:"file"`
	ID     string   `json:"id,omitempty"`
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

// ValidationReport summarizes a directory-wide plugin validation pass.
type ValidationReport struct {
	Dir     string       `json:"dir"`
	Total   int          `json:"total"`
	Passed  int          `json:"passed"`
	Failed  int          `json:"failed"`
	Results []FileResult `json:"results"`
	Plugins []Plugin     `json:"plugins"`
}

// ValidatePlugin checks all schema constraints on a single Plugin manifest.
func ValidatePlugin(p Plugin) []string {
	var errs []string

	if !idPattern.MatchString(p.ID) {
		errs = append(errs, "id must be kebab-case lowercase alphanumeric (e.g. broker-latency-guard)")
	}
	if strings.TrimSpace(p.Name) == "" {
		errs = append(errs, "name must not be empty")
	}
	if !semverPattern.MatchString(p.Version) {
		errs = append(errs, "version must be semantic version X.Y.Z")
	}
	if !validCats[p.Category] {
		errs = append(errs, "category must be one of: latency, journal, alerts, host, container")
	}
	if strings.TrimSpace(p.Author) == "" {
		errs = append(errs, "author must not be empty")
	}
	if len(strings.TrimSpace(p.Description)) < 12 {
		errs = append(errs, "description must be at least 12 characters")
	}
	if !semverPattern.MatchString(p.MinTradeBoxVersion) {
		errs = append(errs, "min_tradebox_version must be semantic version X.Y.Z")
	}
	if len(p.Tags) == 0 {
		errs = append(errs, "tags must include at least one tag")
	}
	if len(p.Triggers) == 0 {
		errs = append(errs, "triggers must include at least one trigger")
	}
	if p.Config == nil {
		errs = append(errs, "config object must be present")
	}
	if len(p.Rules) == 0 {
		errs = append(errs, "rules must contain at least one rule")
	}
	for i, r := range p.Rules {
		if !idPattern.MatchString(r.ID) {
			errs = append(errs, fmt.Sprintf("rules[%d].id must be kebab-case", i))
		}
		if strings.TrimSpace(r.Match) == "" {
			errs = append(errs, fmt.Sprintf("rules[%d].match must not be empty", i))
		}
		if !validSeverities[r.Severity] {
			errs = append(errs, fmt.Sprintf("rules[%d].severity must be info, warn, or critical", i))
		}
		if strings.TrimSpace(r.Action) == "" {
			errs = append(errs, fmt.Sprintf("rules[%d].action must not be empty", i))
		}
	}
	return errs
}

// LoadDir reads and validates every plugin JSON manifest inside dir (excluding schema.json).
func LoadDir(dir string) (ValidationReport, error) {
	report := ValidationReport{
		Dir:     dir,
		Results: []FileResult{},
		Plugins: []Plugin{},
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return report, fmt.Errorf("read plugins dir %q: %w", dir, err)
	}

	var files []string
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if !strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		if strings.EqualFold(name, "schema.json") {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)

	seenIDs := make(map[string]string)

	for _, name := range files {
		report.Total++
		full := filepath.Join(dir, name)
		raw, err := os.ReadFile(full)
		if err != nil {
			report.Failed++
			report.Results = append(report.Results, FileResult{
				File:   name,
				Valid:  false,
				Errors: []string{fmt.Sprintf("read error: %v", err)},
			})
			continue
		}

		var p Plugin
		if err := json.Unmarshal(raw, &p); err != nil {
			report.Failed++
			report.Results = append(report.Results, FileResult{
				File:   name,
				Valid:  false,
				Errors: []string{fmt.Sprintf("invalid JSON: %v", err)},
			})
			continue
		}

		errs := ValidatePlugin(p)
		if prevFile, dup := seenIDs[p.ID]; dup && p.ID != "" {
			errs = append(errs, fmt.Sprintf("duplicate plugin id %q already defined in %s", p.ID, prevFile))
		} else if p.ID != "" {
			seenIDs[p.ID] = name
		}

		if len(errs) > 0 {
			report.Failed++
			report.Results = append(report.Results, FileResult{
				File:   name,
				ID:     p.ID,
				Valid:  false,
				Errors: errs,
			})
			continue
		}

		p.File = name
		report.Passed++
		report.Results = append(report.Results, FileResult{
			File:  name,
			ID:    p.ID,
			Valid: true,
		})
		report.Plugins = append(report.Plugins, p)
	}

	return report, nil
}
