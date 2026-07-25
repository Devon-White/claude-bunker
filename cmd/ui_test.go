package cmd

import (
	"bytes"
	"testing"
)

func TestDiagnosticsGoToStderr(t *testing.T) {
	var out, errb bytes.Buffer
	origOut, origErr := outW, errW
	outW, errW = &out, &errb
	t.Cleanup(func() { outW, errW = origOut, origErr })

	origVerbosity := verbosity
	verbosity = 1
	t.Cleanup(func() { verbosity = origVerbosity })

	info("building")
	verbose("detail")
	success("done")
	warn("careful")

	if out.Len() != 0 {
		t.Errorf("diagnostics must NOT go to stdout; got %q", out.String())
	}
	for _, want := range []string{"building", "detail", "done", "careful"} {
		if !bytes.Contains(errb.Bytes(), []byte(want)) {
			t.Errorf("stderr missing %q; got %q", want, errb.String())
		}
	}
}
