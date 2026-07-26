package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	dockerclient "github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/container"
)

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove orphaned Docker volumes and images",
	Long: `Lists and removes Docker volumes and images created by claude-bunker.

By default, shows all resources grouped by project and asks for confirmation.
Use --force to skip confirmation (useful for scripting).
Use --all to remove all resources without interactive selection.`,
	RunE: runPrune,
}

func init() {
	pruneCmd.Flags().Bool("force", false, "Skip confirmation prompt")
	pruneCmd.Flags().Bool("all", false, "Remove all volumes/images without interactive selection")
	pruneCmd.Flags().Bool("json", false, "Output candidates as JSON (non-interactive; with --force, removes them)")
	pruneCmd.Flags().Bool("dry-run", false, "Show what would be removed without removing anything")
}

// pruneShouldRemove decides whether a prune actually removes resources.
// --dry-run always wins: it forces a list-only (remove nothing) run even
// when --force is also set.
func pruneShouldRemove(force, dryRun bool) bool {
	return force && !dryRun
}

func runPrune(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	dryRun, _ = cmd.Flags().GetBool("dry-run")
	ctx := context.Background()

	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	force, _ := cmd.Flags().GetBool("force")
	all, _ := cmd.Flags().GetBool("all")

	// JSON mode is a separate early-return branch (mirrors status.go / sessions_list.go):
	// non-interactive; DEFAULT is dry-run (list candidates, remove nothing); --force performs
	// removal. --all is meaningless here (JSON operates on all candidates) and is ignored.
	if jsonOut, _ := cmd.Flags().GetBool("json"); jsonOut {
		rep, err := gatherPruneReport(ctx, cli, pruneShouldRemove(force, dryRun))
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	if err := pruneVolumes(ctx, cli, force, all); err != nil {
		return err
	}
	if err := pruneImages(ctx, cli, force, all); err != nil {
		return err
	}

	return nil
}

// pruneSpec defines the callbacks needed to list, label, and remove a resource type.
type pruneSpec[T any] struct {
	// resourceName is the plural noun shown in messages (e.g. "volume", "image").
	resourceName string
	// list fetches all resources from Docker.
	list func(ctx context.Context, cli *dockerclient.Client) ([]T, error)
	// groups partitions items into labelled groups for multi-select.
	// Each group has a display label and a slice of items.
	// For flat lists, return one group per item.
	groups func(items []T) (labels []string, grouped [][]T)
	// label returns the display string for a single item (used in confirm/verbose).
	label func(item T) string
	// remove deletes a single resource.
	remove func(ctx context.Context, cli *dockerclient.Client, item T) error
}

// pruneResources implements the generic list -> select -> confirm -> remove pattern.
func pruneResources[T any](ctx context.Context, cli *dockerclient.Client, force, all bool, spec pruneSpec[T]) error {
	items, err := spec.list(ctx, cli)
	if err != nil {
		warn("Failed to list " + spec.resourceName + "s: " + err.Error())
		return nil
	}

	if len(items) == 0 {
		info("No claude-bunker " + spec.resourceName + "s found.")
		return nil
	}

	groupLabels, grouped := spec.groups(items)

	info(fmt.Sprintf("Found %d %s(s).", len(items), spec.resourceName))

	var selectedIndices []int

	if all || dryRun || len(groupLabels) == 1 {
		for i := range groupLabels {
			selectedIndices = append(selectedIndices, i)
		}
	} else if !isTTY() {
		warn("Non-interactive terminal detected. Use --all to select all, or --force to skip prompts.")
		return nil
	} else {
		options := make([]huh.Option[int], len(groupLabels))
		for i, lbl := range groupLabels {
			options[i] = huh.NewOption(lbl, i)
		}

		err := huh.NewForm(
			huh.NewGroup(
				huh.NewMultiSelect[int]().
					Title("Select " + spec.resourceName + "s to remove").
					Description("Space to toggle, Enter to confirm").
					Options(options...).
					Value(&selectedIndices),
			),
		).Run()

		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				info("Aborted.")
				return nil
			}
			warn("Selection failed: " + err.Error())
			return nil
		}
	}

	var toRemove []T
	for _, idx := range selectedIndices {
		toRemove = append(toRemove, grouped[idx]...)
	}

	if len(toRemove) == 0 {
		info("Nothing to remove.")
		return nil
	}

	if dryRun {
		for _, item := range toRemove {
			planf("would remove %s %s", spec.resourceName, spec.label(item))
		}
		return nil
	}

	if !force {
		ok, err := confirmAction(fmt.Sprintf("Remove %d %s(s)?", len(toRemove), spec.resourceName))
		if err != nil {
			return err
		}
		if !ok {
			info("Aborted.")
			return nil
		}
	}

	removed := 0
	for _, item := range toRemove {
		if err := spec.remove(ctx, cli, item); err != nil {
			warn(fmt.Sprintf("Could not remove %s (may be in use).", spec.label(item)))
		} else {
			verbose("Removed: " + spec.label(item))
			removed++
		}
	}

	success(fmt.Sprintf("Pruned %d %s(s).", removed, spec.resourceName))
	return nil
}

func pruneVolumes(ctx context.Context, cli *dockerclient.Client, force, all bool) error {
	return pruneResources(ctx, cli, force, all, pruneSpec[container.BunkerVolume]{
		resourceName: "volume",
		list:         container.ListBunkerVolumesDetailed,
		groups: func(items []container.BunkerVolume) ([]string, [][]container.BunkerVolume) {
			projects := map[string][]container.BunkerVolume{}
			var projectOrder []string
			for _, v := range items {
				if _, seen := projects[v.Project]; !seen {
					projectOrder = append(projectOrder, v.Project)
				}
				projects[v.Project] = append(projects[v.Project], v)
			}
			labels := make([]string, len(projectOrder))
			grouped := make([][]container.BunkerVolume, len(projectOrder))
			for i, p := range projectOrder {
				labels[i] = fmt.Sprintf("%s (%d volume(s))", p, len(projects[p]))
				grouped[i] = projects[p]
			}
			return labels, grouped
		},
		label: func(v container.BunkerVolume) string { return v.Name },
		remove: func(ctx context.Context, cli *dockerclient.Client, v container.BunkerVolume) error {
			return container.RemoveVolume(ctx, cli, v.Name)
		},
	})
}

func pruneImages(ctx context.Context, cli *dockerclient.Client, force, all bool) error {
	return pruneResources(ctx, cli, force, all, pruneSpec[container.BunkerImage]{
		resourceName: "image",
		list:         container.ListBunkerImages,
		groups: func(items []container.BunkerImage) ([]string, [][]container.BunkerImage) {
			labels := make([]string, len(items))
			grouped := make([][]container.BunkerImage, len(items))
			for i, img := range items {
				sizeMB := float64(img.Size) / 1024 / 1024
				labels[i] = fmt.Sprintf("%s (%.0f MB)", img.Tag, sizeMB)
				grouped[i] = []container.BunkerImage{img}
			}
			return labels, grouped
		},
		label: func(img container.BunkerImage) string { return img.Tag },
		remove: func(ctx context.Context, cli *dockerclient.Client, img container.BunkerImage) error {
			return container.RemoveImageByTag(ctx, cli, img.Tag)
		},
	})
}

// pruneReport is the machine-readable result of `prune --json`. When DryRun is
// true nothing was removed (the default); with --json --force items are removed
// and the *Removed counts / BytesReclaimed reflect it.
type pruneReport struct {
	DryRun         bool                `json:"dry_run"`
	Volumes        []pruneVolumeResult `json:"volumes"`
	Images         []pruneImageResult  `json:"images"`
	VolumesRemoved int                 `json:"volumes_removed"`
	ImagesRemoved  int                 `json:"images_removed"`
	// BytesReclaimed is images-only: BunkerVolume has no Size field, so volume
	// bytes cannot be reported without extra Docker calls (out of scope).
	BytesReclaimed int64    `json:"bytes_reclaimed"`
	Errors         []string `json:"errors,omitempty"`
}

type pruneVolumeResult struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Project string `json:"project"`
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

type pruneImageResult struct {
	ID      string `json:"id"`
	Tag     string `json:"tag"`
	Size    int64  `json:"size"` // bytes; lets a dry-run consumer sum a "reclaimable" total
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// gatherPruneReport lists claude-bunker volumes and images and, when remove is
// true, removes them, assembling a pruneReport. Unlike the interactive path
// (which warns and returns nil on a list failure), a list error here is returned
// as a real error so runPrune exits non-zero. It prints nothing; per-item removal
// failures land in the report's Errors slice.
func gatherPruneReport(ctx context.Context, cli *dockerclient.Client, remove bool) (pruneReport, error) {
	vols, err := container.ListBunkerVolumesDetailed(ctx, cli)
	if err != nil {
		return pruneReport{}, fmt.Errorf("failed to list volumes: %w", err)
	}
	imgs, err := container.ListBunkerImages(ctx, cli)
	if err != nil {
		return pruneReport{}, fmt.Errorf("failed to list images: %w", err)
	}
	removeVol := func(v container.BunkerVolume) error {
		return container.RemoveVolume(ctx, cli, v.Name)
	}
	removeImg := func(img container.BunkerImage) error {
		return container.RemoveImageByTag(ctx, cli, img.Tag)
	}
	return buildPruneReport(vols, imgs, remove, removeVol, removeImg), nil
}

// buildPruneReport is the pure core of gatherPruneReport. When remove is false it
// lists candidates only (DryRun=true, nothing removed). When remove is true it
// invokes removeVol/removeImg per item, recording each item's Removed/Error,
// tallying VolumesRemoved/ImagesRemoved and (images only) BytesReclaimed; per-item
// failures are appended to Errors. It is decoupled from Docker so callers/tests can
// inject fake remove callbacks.
func buildPruneReport(
	vols []container.BunkerVolume,
	imgs []container.BunkerImage,
	remove bool,
	removeVol func(container.BunkerVolume) error,
	removeImg func(container.BunkerImage) error,
) pruneReport {
	rep := pruneReport{DryRun: !remove}

	for _, v := range vols {
		res := pruneVolumeResult{Name: v.Name, Kind: v.Kind, Project: v.Project}
		if remove {
			if err := removeVol(v); err != nil {
				res.Error = err.Error()
				rep.Errors = append(rep.Errors, fmt.Sprintf("volume %s: %s", v.Name, err.Error()))
			} else {
				res.Removed = true
				rep.VolumesRemoved++
			}
		}
		rep.Volumes = append(rep.Volumes, res)
	}

	for _, img := range imgs {
		res := pruneImageResult{ID: img.ID, Tag: img.Tag, Size: img.Size}
		if remove {
			if err := removeImg(img); err != nil {
				res.Error = err.Error()
				rep.Errors = append(rep.Errors, fmt.Sprintf("image %s: %s", img.Tag, err.Error()))
			} else {
				res.Removed = true
				rep.ImagesRemoved++
				rep.BytesReclaimed += img.Size
			}
		}
		rep.Images = append(rep.Images, res)
	}

	return rep
}
