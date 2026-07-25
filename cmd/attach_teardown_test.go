package cmd

import (
	"errors"
	"testing"
)

func TestShouldStopAfterSession(t *testing.T) {
	tests := []struct {
		name        string
		keep        bool
		otherActive bool
		checkErr    error
		force       bool
		wantStop    bool
	}{
		{"keep leaves running", true, false, nil, false, false},
		{"no others -> stop", false, false, nil, false, true},
		{"others active -> leave", false, true, nil, false, false},
		{"check error -> leave (fail closed)", false, false, errors.New("x"), false, false},
		{"check error + force -> stop", false, false, errors.New("x"), true, true},
		{"others active + force -> stop", false, true, nil, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldStopAfterSession(tt.keep, tt.otherActive, tt.checkErr, tt.force)
			if got != tt.wantStop {
				t.Errorf("shouldStopAfterSession = %v, want %v", got, tt.wantStop)
			}
		})
	}
}
