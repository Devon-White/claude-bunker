package cmd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// buildPruneReport is the pure core of gatherPruneReport: given already-listed
// volumes/images plus remove callbacks, it assembles the report. These tests
// inject fake remove funcs so no Docker daemon is required.
func TestBuildPruneReport(t *testing.T) {
	vols := []container.BunkerVolume{
		{Name: "claude-bunker-history-projA", Kind: "bashhistory", Project: "projA"},
		{Name: "claude-bunker-config-projA", Kind: "config", Project: "projA"},
	}
	imgs := []container.BunkerImage{
		{ID: "aaaaaaaaaaaa", Tag: "claude-bunker:aaa", Size: 100},
		{ID: "bbbbbbbbbbbb", Tag: "claude-bunker:bbb", Size: 200},
	}

	tests := []struct {
		name           string
		remove         bool
		failImageTag   string // tag whose removal fails ("" = none)
		wantDryRun     bool
		wantVolRemoved int
		wantImgRemoved int
		wantBytes      int64
		wantErrCount   int
		wantVolCalls   int
		wantImgCalls   int
	}{
		{
			name:       "dry-run removes nothing",
			remove:     false,
			wantDryRun: true,
			// all tallies zero, remove funcs never called
		},
		{
			name:           "force removes all",
			remove:         true,
			wantDryRun:     false,
			wantVolRemoved: 2,
			wantImgRemoved: 2,
			wantBytes:      300,
			wantVolCalls:   2,
			wantImgCalls:   2,
		},
		{
			name:           "force with one image failure",
			remove:         true,
			failImageTag:   "claude-bunker:bbb",
			wantDryRun:     false,
			wantVolRemoved: 2,
			wantImgRemoved: 1,
			wantBytes:      100, // only the successfully-removed image counts
			wantErrCount:   1,
			wantVolCalls:   2,
			wantImgCalls:   2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			volCalls, imgCalls := 0, 0
			removeVol := func(v container.BunkerVolume) error {
				volCalls++
				return nil
			}
			removeImg := func(img container.BunkerImage) error {
				imgCalls++
				if img.Tag == tc.failImageTag {
					return errors.New("in use")
				}
				return nil
			}

			rep := buildPruneReport(vols, imgs, tc.remove, removeVol, removeImg)

			if rep.DryRun != tc.wantDryRun {
				t.Errorf("DryRun = %v, want %v", rep.DryRun, tc.wantDryRun)
			}
			if rep.VolumesRemoved != tc.wantVolRemoved {
				t.Errorf("VolumesRemoved = %d, want %d", rep.VolumesRemoved, tc.wantVolRemoved)
			}
			if rep.ImagesRemoved != tc.wantImgRemoved {
				t.Errorf("ImagesRemoved = %d, want %d", rep.ImagesRemoved, tc.wantImgRemoved)
			}
			if rep.BytesReclaimed != tc.wantBytes {
				t.Errorf("BytesReclaimed = %d, want %d", rep.BytesReclaimed, tc.wantBytes)
			}
			if len(rep.Errors) != tc.wantErrCount {
				t.Errorf("len(Errors) = %d, want %d (%v)", len(rep.Errors), tc.wantErrCount, rep.Errors)
			}
			if volCalls != tc.wantVolCalls {
				t.Errorf("removeVol called %d times, want %d", volCalls, tc.wantVolCalls)
			}
			if imgCalls != tc.wantImgCalls {
				t.Errorf("removeImg called %d times, want %d", imgCalls, tc.wantImgCalls)
			}

			// The report always lists every candidate, in both modes.
			if len(rep.Volumes) != len(vols) {
				t.Errorf("len(Volumes) = %d, want %d", len(rep.Volumes), len(vols))
			}
			if len(rep.Images) != len(imgs) {
				t.Errorf("len(Images) = %d, want %d", len(rep.Images), len(imgs))
			}

			// In dry-run mode every item must be Removed=false (zero mutations).
			if !tc.remove {
				for _, v := range rep.Volumes {
					if v.Removed {
						t.Errorf("dry-run volume %s marked Removed", v.Name)
					}
				}
				for _, im := range rep.Images {
					if im.Removed {
						t.Errorf("dry-run image %s marked Removed", im.Tag)
					}
				}
			}
		})
	}
}

// TestPruneReportJSON pins the snake_case wire format (mirrors TestStatusInfoJSON).
func TestPruneReportJSON(t *testing.T) {
	rep := pruneReport{
		DryRun: true,
		Volumes: []pruneVolumeResult{
			{Name: "claude-bunker-config-projA", Kind: "config", Project: "projA"},
		},
		Images: []pruneImageResult{
			{ID: "aaaaaaaaaaaa", Tag: "claude-bunker:aaa", Size: 100},
		},
	}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"dry_run", "volumes", "images", "volumes_removed", "images_removed", "bytes_reclaimed"} {
		if _, ok := back[k]; !ok {
			t.Errorf("prune JSON missing key %q: %s", k, data)
		}
	}
	// Errors is `omitempty`: an empty report must not emit an "errors" key.
	if _, ok := back["errors"]; ok {
		t.Errorf("empty Errors should be omitted, got: %s", data)
	}
}
