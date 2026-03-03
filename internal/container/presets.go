package container

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
)

// LanguagePreset defines a language runtime that the init wizard can offer.
type LanguagePreset struct {
	Label          string   // Display name ("Node.js")
	FeatureRepo    string   // OCI repo without tag ("ghcr.io/devcontainers/features/node")
	VersionOption  string   // Feature option name for language version ("version")
	Domains        []string // Package manager domains to allowlist
	CommonVersions []string // Fallback versions used when the API is unreachable
	DefaultVersion string   // Fallback if version selection is skipped
	EOLProduct     string   // endoflife.date product name ("nodejs", "python", …)
}

// Presets is the ordered list of language presets shown in the init wizard.
var Presets = []LanguagePreset{
	{
		Label:          "Node.js",
		FeatureRepo:    "ghcr.io/devcontainers/features/node",
		VersionOption:  "version",
		Domains:        []string{"registry.npmjs.org"},
		CommonVersions: []string{"22", "20", "18"},
		DefaultVersion: "lts",
		EOLProduct:     "nodejs",
	},
	{
		Label:          "Python",
		FeatureRepo:    "ghcr.io/devcontainers/features/python",
		VersionOption:  "version",
		Domains:        []string{"pypi.org", "files.pythonhosted.org"},
		CommonVersions: []string{"3.13", "3.12", "3.11", "3.10"},
		DefaultVersion: "latest",
		EOLProduct:     "python",
	},
	{
		Label:          "Go",
		FeatureRepo:    "ghcr.io/devcontainers/features/go",
		VersionOption:  "version",
		Domains:        []string{"proxy.golang.org", "sum.golang.org", "storage.googleapis.com"},
		CommonVersions: []string{"1.24", "1.23", "1.22"},
		DefaultVersion: "latest",
		EOLProduct:     "go",
	},
	{
		Label:          "Rust",
		FeatureRepo:    "ghcr.io/devcontainers/features/rust",
		VersionOption:  "version",
		Domains:        []string{"crates.io", "static.crates.io", "index.crates.io"},
		CommonVersions: []string{"latest", "1.84", "1.83", "1.82"},
		DefaultVersion: "latest",
		EOLProduct:     "rust",
	},
	{
		Label:          "Java",
		FeatureRepo:    "ghcr.io/devcontainers/features/java",
		VersionOption:  "version",
		Domains:        []string{"repo1.maven.org", "repo.maven.apache.org", "plugins.gradle.org", "services.gradle.org"},
		CommonVersions: []string{"21", "17", "11"},
		DefaultVersion: "lts",
		EOLProduct:     "eclipse-temurin",
	},
	{
		Label:          ".NET",
		FeatureRepo:    "ghcr.io/devcontainers/features/dotnet",
		VersionOption:  "version",
		Domains:        []string{"api.nuget.org", "dotnetcli.azureedge.net"},
		CommonVersions: []string{"9.0", "8.0", "7.0"},
		DefaultVersion: "latest",
		EOLProduct:     "dotnet",
	},
	{
		Label:          "Ruby",
		FeatureRepo:    "ghcr.io/devcontainers/features/ruby",
		VersionOption:  "version",
		Domains:        []string{"rubygems.org", "index.rubygems.org"},
		CommonVersions: []string{"3.4", "3.3", "3.2"},
		DefaultVersion: "latest",
		EOLProduct:     "ruby",
	},
	{
		Label:          "PHP",
		FeatureRepo:    "ghcr.io/devcontainers/features/php",
		VersionOption:  "version",
		Domains:        []string{"getcomposer.org", "repo.packagist.org", "packagist.org"},
		CommonVersions: []string{"8.4", "8.3", "8.2"},
		DefaultVersion: "latest",
		EOLProduct:     "php",
	},
}

// EOLValue handles the endoflife.date polymorphic eol/lts field.
// The API returns either the JSON boolean false (not yet EOL) or a
// "YYYY-MM-DD" date string (end-of-life date, past or future).
type EOLValue struct {
	hasDate bool
	date    string // "YYYY-MM-DD", only valid when hasDate is true
}

func (e *EOLValue) UnmarshalJSON(b []byte) error {
	// Try boolean first (false = still supported, true = EOL with no date).
	var boolVal bool
	if json.Unmarshal(b, &boolVal) == nil {
		e.hasDate = boolVal // true means EOL but no specific date
		return nil
	}
	// Fall through to string date.
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("eol field: expected bool or date string, got %s", b)
	}
	e.hasDate = true
	e.date = s
	return nil
}

// IsExpired reports whether the EOL date has passed.
// Returns false if no EOL date is set (i.e., still actively supported).
// Compares ISO date strings to avoid timezone/time-of-day edge cases.
func (e EOLValue) IsExpired() bool {
	if !e.hasDate || e.date == "" {
		return false
	}
	today := time.Now().UTC().Format("2006-01-02")
	return today > e.date
}

// eolRelease is one cycle entry from the endoflife.date API.
type eolRelease struct {
	Cycle string   `json:"cycle"`
	EOL   EOLValue `json:"eol"`
}

// FetchSupportedVersions queries endoflife.date for the given preset and
// returns cycle strings for releases that are not yet end-of-life, in the
// order the API returns them (newest first). Returns an error if the request
// fails or the response cannot be parsed, in which case the caller should
// fall back to CommonVersions.
func FetchSupportedVersions(preset LanguagePreset) ([]string, error) {
	if preset.EOLProduct == "" {
		return nil, fmt.Errorf("no EOLProduct configured for %s", preset.Label)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://endoflife.date/api/%s.json", preset.EOLProduct)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("endoflife.date: %s returned %d", preset.EOLProduct, resp.StatusCode)
	}

	var releases []eolRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("endoflife.date: decoding %s: %w", preset.EOLProduct, err)
	}

	var versions []string
	for _, r := range releases {
		if !r.EOL.IsExpired() {
			versions = append(versions, r.Cycle)
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("endoflife.date: no active releases found for %s", preset.EOLProduct)
	}
	return versions, nil
}

// majorTagRe matches tags that are purely numeric major versions (e.g. "1", "2").
var majorTagRe = regexp.MustCompile(`^[0-9]+$`)

// LatestFeatureTag queries the OCI registry for the highest major tag
// of a devcontainer feature. Returns "1" on error (safe default since
// virtually all devcontainer features are published at major version 1).
func LatestFeatureTag(repo string) string {
	tags, err := crane.ListTags(repo)
	if err != nil {
		return "1"
	}

	var majors []int
	for _, t := range tags {
		if majorTagRe.MatchString(t) {
			if n, err := strconv.Atoi(t); err == nil {
				majors = append(majors, n)
			}
		}
	}

	if len(majors) == 0 {
		return "1"
	}

	sort.Sort(sort.Reverse(sort.IntSlice(majors)))
	return strconv.Itoa(majors[0])
}
