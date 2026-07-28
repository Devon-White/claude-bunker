package container

import "testing"

func TestEgressProxySourcesIncludesMainAndGoMod(t *testing.T) {
	srcs := EgressProxySources()
	var hasMain, hasMod bool
	for _, f := range srcs {
		if f.Name == "egressproxy/main.go" {
			hasMain = true
		}
		if f.Name == "egressproxy/go.mod" {
			hasMod = true
		}
		if len(f.Name) > 8 && f.Name[len(f.Name)-8:] == "_test.go" {
			t.Errorf("test file leaked into build context: %s", f.Name)
		}
	}
	if !hasMain {
		t.Error("egressproxy/main.go missing from build context")
	}
	if !hasMod {
		t.Error("synthetic egressproxy/go.mod missing from build context")
	}
}
