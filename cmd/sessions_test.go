package cmd

import "testing"

func TestSessionsIntervalFlagDefault(t *testing.T) {
	f := sessionsCmd.Flags().Lookup("interval")
	if f == nil {
		t.Fatal("sessions command missing --interval flag")
	}
	if f.DefValue != "3s" {
		t.Errorf("--interval default = %q, want %q", f.DefValue, "3s")
	}
}
