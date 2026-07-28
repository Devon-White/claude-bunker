package main

import "testing"

func TestLoadMaskingAbsentIsNil(t *testing.T) {
	rules, err := LoadMasking("/nonexistent/masking.json")
	if err != nil {
		t.Fatalf("absent masking file must not error, got %v", err)
	}
	if rules != nil {
		t.Fatalf("absent masking file must yield nil rules, got %v", rules)
	}
}

func TestLoadMaskingParses(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "masking.json", `[{"sentinel":"S","secret":"R","hosts":["api.anthropic.com"],"headers":["x-api-key"]}]`)
	rules, err := LoadMasking(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Sentinel != "S" || rules[0].Secret != "R" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}
