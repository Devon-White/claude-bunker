package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	dockerclient "github.com/docker/docker/client"

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

// pruneResources under dryRun must plan every candidate and remove nothing,
// without prompting. A fake spec records remove() calls; nil cli is never used
// because list/remove ignore it and the dry-run branch returns before removal.
func TestPruneResources_DryRunPlansWithoutRemoving(t *testing.T) {
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

	var removeCalls []string
	spec := pruneSpec[string]{
		resourceName: "image",
		list: func(_ context.Context, _ *dockerclient.Client) ([]string, error) {
			return []string{"img-a", "img-b"}, nil
		},
		groups: func(items []string) ([]string, [][]string) {
			labels := make([]string, len(items))
			grouped := make([][]string, len(items))
			for i, it := range items {
				labels[i] = it
				grouped[i] = []string{it}
			}
			return labels, grouped
		},
		label: func(s string) string { return s },
		remove: func(_ context.Context, _ *dockerclient.Client, s string) error {
			removeCalls = append(removeCalls, s)
			return nil
		},
	}

	if err := pruneResources(context.Background(), nil, false, false, spec); err != nil {
		t.Fatalf("pruneResources: %v", err)
	}
	if len(removeCalls) != 0 {
		t.Fatalf("dry-run must remove nothing; removed %v", removeCalls)
	}
	out := buf.String()
	for _, want := range []string{"would remove image img-a", "would remove image img-b"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q; got %q", want, out)
		}
	}
}

func TestPruneShouldRemove(t *testing.T) {
	cases := []struct {
		force, dryRun, want bool
	}{
		{force: false, dryRun: false, want: false}, // no force → list only
		{force: true, dryRun: false, want: true},   // force, no dry-run → remove
		{force: true, dryRun: true, want: false},   // dry-run WINS over force
		{force: false, dryRun: true, want: false},  // dry-run, no force → list only
	}
	for _, tc := range cases {
		if got := pruneShouldRemove(tc.force, tc.dryRun); got != tc.want {
			t.Errorf("pruneShouldRemove(force=%v, dryRun=%v) = %v, want %v", tc.force, tc.dryRun, got, tc.want)
		}
	}
}
