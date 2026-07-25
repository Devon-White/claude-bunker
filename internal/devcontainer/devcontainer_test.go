package devcontainer

import "testing"

func TestParseAndToProjectConfig(t *testing.T) {
	in := `{
  "name": "demo",
  "image": "mcr.microsoft.com/devcontainers/base:debian",
  "features": {
    "ghcr.io/devcontainers/features/node:1": { "version": "20" }
  },
  "containerEnv": { "NODE_ENV": "development" },
  "postStartCommand": "npm install",
  "onCreateCommand": ["echo a", "echo b"],
  "customizations": {
    "claude-bunker": {
      "exclude": ["secrets/"],
      "allowDomains": ["registry.npmjs.org"],
      "apt": ["ripgrep"],
      "plugins": "project",
      "seedHistory": false,
      "workspace": "./packages/api"
    }
  }
}`
	dc, err := Parse([]byte(in), nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg := ToProjectConfig(dc)

	if _, ok := cfg.Features["ghcr.io/devcontainers/features/node:1"]; !ok {
		t.Errorf("feature not mapped: %+v", cfg.Features)
	}
	if cfg.Features["ghcr.io/devcontainers/features/node:1"]["version"] != "20" {
		t.Errorf("feature options not mapped: %+v", cfg.Features)
	}
	if cfg.Env["NODE_ENV"] != "development" {
		t.Errorf("containerEnv not mapped: %+v", cfg.Env)
	}
	if cfg.PostStartCommand != "npm install" {
		t.Errorf("postStart = %q", cfg.PostStartCommand)
	}
	if cfg.OnCreateCommand != "echo a && echo b" {
		t.Errorf("onCreate array not joined: %q", cfg.OnCreateCommand)
	}
	if len(cfg.Exclude) != 1 || cfg.Exclude[0] != "secrets/" {
		t.Errorf("exclude = %+v", cfg.Exclude)
	}
	if len(cfg.AllowDomains) != 1 || cfg.AllowDomains[0] != "registry.npmjs.org" {
		t.Errorf("allowDomains = %+v", cfg.AllowDomains)
	}
	if len(cfg.Apt) != 1 || cfg.Apt[0] != "ripgrep" {
		t.Errorf("apt = %+v", cfg.Apt)
	}
	if cfg.Plugins != "project" {
		t.Errorf("plugins = %q", cfg.Plugins)
	}
	if cfg.SeedHistory == nil || *cfg.SeedHistory != false {
		t.Errorf("seedHistory not mapped: %v", cfg.SeedHistory)
	}
	if cfg.Workspace != "./packages/api" {
		t.Errorf("workspace = %q", cfg.Workspace)
	}
}

func TestToProjectConfig_FeatureShorthand(t *testing.T) {
	dc := DevContainer{Features: map[string]interface{}{
		"ghcr.io/x/enabled:1":  map[string]interface{}{"version": "1"},
		"ghcr.io/x/disabled:1": false,
		"ghcr.io/x/bare:1":     true,
	}}
	cfg := ToProjectConfig(dc)
	if _, ok := cfg.Features["ghcr.io/x/disabled:1"]; ok {
		t.Error("a false-valued (disabled) feature must be skipped")
	}
	if _, ok := cfg.Features["ghcr.io/x/enabled:1"]; !ok {
		t.Error("object-valued feature must be present")
	}
	if opts, ok := cfg.Features["ghcr.io/x/bare:1"]; !ok || len(opts) != 0 {
		t.Errorf("true-valued feature must be present with empty options: %v", cfg.Features["ghcr.io/x/bare:1"])
	}
}

func TestCommandToString(t *testing.T) {
	cases := map[string]string{
		`"a"`:               "a",
		`["a","b"]`:         "a && b",
		`{"x":"a","y":"b"}`: "", // object form: no single command; empty is acceptable
	}
	for in, want := range cases {
		got := commandToString([]byte(in))
		if in == `{"x":"a","y":"b"}` {
			continue // object form is implementation-defined; skip strict check
		}
		if got != want {
			t.Errorf("commandToString(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestParse_Malformed(t *testing.T) {
	if _, err := Parse([]byte("{not valid json"), nil); err == nil {
		t.Error("malformed devcontainer.json must return an error")
	}
}

func TestToProjectConfig_MissingCustomizations(t *testing.T) {
	dc, err := Parse([]byte(`{"image":"x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ToProjectConfig(dc) // must not panic; extras are zero-value
	if cfg.Exclude != nil || cfg.Plugins != "" {
		t.Errorf("missing customizations should yield zero extras: %+v", cfg)
	}
}

func TestCommandToString_ObjectAndNull(t *testing.T) {
	if got := commandToString([]byte(`{"a":"x"}`)); got != "" {
		t.Errorf("object command → empty, got %q", got)
	}
	if got := commandToString([]byte(`null`)); got != "" {
		t.Errorf("null command → empty, got %q", got)
	}
	if got := commandToString(nil); got != "" {
		t.Errorf("nil command → empty, got %q", got)
	}
}
