package devcontainer

import (
	"encoding/json"
	"testing"
)

func TestPreprocess(t *testing.T) {
	env := func(name string) (string, bool) {
		if name == "HOME" {
			return "/home/dev", true
		}
		return "", false
	}

	in := `{
  // line comment
  "name": "x", /* block */ "image": "y", // trailing line
  "url": "http://not-a-comment.example/path", // real comment
  "slashes": "a//b/*c*/d",
  "home": "${localEnv:HOME}",
  "missing": "${localEnv:NOPE}",
  "withDefault": "${localEnv:NOPE:-fallback}",
  "arr": [1, 2, 3,],
}`
	got := preprocess([]byte(in), env)

	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("preprocessed output is not valid JSON: %v\n%s", err, got)
	}
	if m["name"] != "x" || m["image"] != "y" {
		t.Errorf("comment stripping altered values: %+v", m)
	}
	if m["url"] != "http://not-a-comment.example/path" {
		t.Errorf("stripped // inside a string literal: %v", m["url"])
	}
	if m["slashes"] != "a//b/*c*/d" {
		t.Errorf("stripped comment-like sequence inside string: %v", m["slashes"])
	}
	if m["home"] != "/home/dev" {
		t.Errorf("localEnv HOME not substituted: %v", m["home"])
	}
	if m["missing"] != "" {
		t.Errorf("unset localEnv should be empty: %v", m["missing"])
	}
	if m["withDefault"] != "fallback" {
		t.Errorf("localEnv default not used: %v", m["withDefault"])
	}
	arr, _ := m["arr"].([]interface{})
	if len(arr) != 3 {
		t.Errorf("trailing comma handling broke array: %v", m["arr"])
	}
}

func TestPreprocess_NilEnv(t *testing.T) {
	got := preprocess([]byte(`{"x":"${localEnv:ANY:-d}"}`), nil)
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["x"] != "d" {
		t.Errorf("nil env should use default: %v", m["x"])
	}
}
