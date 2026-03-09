package container

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPresetsIntegrity(t *testing.T) {
	if len(Presets) == 0 {
		t.Fatal("Presets slice is empty")
	}

	seen := map[string]bool{}
	for i, p := range Presets {
		if p.Label == "" {
			t.Errorf("Presets[%d]: Label is empty", i)
		}
		if p.FeatureRepo == "" {
			t.Errorf("Presets[%d] (%s): FeatureRepo is empty", i, p.Label)
		}
		if p.VersionOption == "" {
			t.Errorf("Presets[%d] (%s): VersionOption is empty", i, p.Label)
		}
		if len(p.Domains) == 0 {
			t.Errorf("Presets[%d] (%s): Domains is empty", i, p.Label)
		}
		if len(p.CommonVersions) == 0 {
			t.Errorf("Presets[%d] (%s): CommonVersions is empty", i, p.Label)
		}
		if p.EOLProduct == "" {
			t.Errorf("Presets[%d] (%s): EOLProduct is empty", i, p.Label)
		}
		if seen[p.Label] {
			t.Errorf("Presets[%d]: duplicate Label %q", i, p.Label)
		}
		seen[p.Label] = true
	}
}

func TestMajorTagRegex(t *testing.T) {
	tests := []struct {
		tag   string
		match bool
	}{
		{"1", true},
		{"2", true},
		{"10", true},
		{"0", true},
		{"1.0", false},
		{"1.0.0", false},
		{"latest", false},
		{"v1", false},
		{"", false},
		{"1-beta", false},
	}

	for _, tt := range tests {
		got := majorTagRe.MatchString(tt.tag)
		if got != tt.match {
			t.Errorf("majorTagRe.MatchString(%q) = %v, want %v", tt.tag, got, tt.match)
		}
	}
}

func TestLatestFeatureTag_FiltersMajors(t *testing.T) {
	// LatestFeatureTag hits the network, so we test the filtering logic
	// indirectly through the regex. The function defaults to "1" on error
	// which is the correct fallback.
	result := LatestFeatureTag("invalid.example.com/does-not-exist")
	if result != "1" {
		t.Errorf("LatestFeatureTag with invalid repo = %q, want %q", result, "1")
	}
}

func TestEOLValue_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantExpired bool
	}{
		// false = still actively supported, not expired (the common API value)
		{"bool false", `false`, false},
		// future date = not yet expired
		{"future date", `"2099-01-01"`, false},
		// past date = expired
		{"past date", `"2020-01-01"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v EOLValue
			if err := json.Unmarshal([]byte(tt.input), &v); err != nil {
				t.Fatalf("UnmarshalJSON(%s): unexpected error: %v", tt.input, err)
			}
			if got := v.IsExpired(); got != tt.wantExpired {
				t.Errorf("IsExpired() = %v, want %v", got, tt.wantExpired)
			}
		})
	}
}

func TestEOLValue_IsExpired_Today(t *testing.T) {
	// A date equal to today should not yet be considered expired
	// (we use After, not AfterOrEqual).
	today := time.Now().UTC().Format("2006-01-02")
	input := `"` + today + `"`

	var v EOLValue
	if err := json.Unmarshal([]byte(input), &v); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if v.IsExpired() {
		t.Errorf("IsExpired() = true for today's date %s, want false", today)
	}
}

func TestEOLValue_UnmarshalJSON_Invalid(t *testing.T) {
	var v EOLValue
	if err := json.Unmarshal([]byte(`123`), &v); err == nil {
		t.Error("expected error for numeric input, got nil")
	}
}

func TestFetchSupportedVersions_NoProduct(t *testing.T) {
	_, err := FetchSupportedVersions(LanguagePreset{Label: "Empty"})
	if err == nil {
		t.Error("expected error for empty EOLProduct, got nil")
	}
}

func TestFetchSupportedVersions_BadProduct(t *testing.T) {
	// An unknown product should return a non-200 or empty result, not panic.
	_, err := FetchSupportedVersions(LanguagePreset{
		Label:      "Nonexistent",
		EOLProduct: "this-product-does-not-exist-xyzzy",
	})
	if err == nil {
		t.Error("expected error for unknown product, got nil")
	}
}
