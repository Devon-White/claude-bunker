package cmd

import "testing"

func TestRenderVersionJSON(t *testing.T) {
	out := renderVersionJSON("1.2.3")
	if out != `{"version":"1.2.3"}` {
		t.Errorf("renderVersionJSON = %q", out)
	}
}
