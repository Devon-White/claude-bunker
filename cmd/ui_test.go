package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestShouldUseColor(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	tests := []struct {
		name        string
		noColorFlag bool
		vars        map[string]string
		stderrTTY   bool
		want        bool
	}{
		{"tty no env → color", false, nil, true, true},
		{"not a tty → no color", false, nil, false, false},
		{"NO_COLOR set (even empty) → no color", false, map[string]string{"NO_COLOR": ""}, true, false},
		{"CLICOLOR=0 → no color", false, map[string]string{"CLICOLOR": "0"}, true, false},
		{"--no-color → no color", true, nil, true, false},
		{"CLICOLOR_FORCE overrides non-tty", false, map[string]string{"CLICOLOR_FORCE": "1"}, false, true},
		{"NO_COLOR beats CLICOLOR_FORCE", false, map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseColor(tt.noColorFlag, env(tt.vars), tt.stderrTTY); got != tt.want {
				t.Errorf("shouldUseColor = %v, want %v", got, tt.want)
			}
		})
	}
}

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

func TestPlanAlwaysPrintsIgnoringQuiet(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origV := verbosity
	verbosity = -1 // --quiet: info/warn/success are suppressed, plan must NOT be
	t.Cleanup(func() { verbosity = origV })

	plan("would do a thing")
	planf("would remove %s", "widget")

	out := buf.String()
	if !strings.Contains(out, "would do a thing") {
		t.Errorf("plan() must print even under --quiet; got %q", out)
	}
	if !strings.Contains(out, "would remove widget") {
		t.Errorf("planf() must print even under --quiet; got %q", out)
	}
}

func TestSuccessSuppressedUnderDryRun(t *testing.T) {
	var buf bytes.Buffer
	origErr := errW
	errW = &buf
	t.Cleanup(func() { errW = origErr })

	origV := verbosity
	verbosity = 0
	t.Cleanup(func() { verbosity = origV })

	origDry := dryRun
	dryRun = true
	t.Cleanup(func() { dryRun = origDry })

	success("Saved something")
	if buf.Len() != 0 {
		t.Errorf("success() must not fire under dryRun; got %q", buf.String())
	}
}

func TestConfirmAction_NonInteractiveErrors(t *testing.T) {
	orig := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = orig })

	ok, err := confirmAction("Proceed?")
	if ok {
		t.Error("must not return true in a non-interactive context")
	}
	if !errors.Is(err, errNonInteractiveConfirm) {
		t.Errorf("must return errNonInteractiveConfirm, got %v", err)
	}
}
