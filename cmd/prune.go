package cmd

import (
	"context"
	"errors"
	"fmt"

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
}

func runPrune(cmd *cobra.Command, args []string) error {
	initVerbosity(cmd)
	ctx := context.Background()

	cli, err := container.NewClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	force, _ := cmd.Flags().GetBool("force")
	all, _ := cmd.Flags().GetBool("all")

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

	if all || len(groupLabels) == 1 {
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
		label:  func(v container.BunkerVolume) string { return v.Name },
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
		label:  func(img container.BunkerImage) string { return img.Tag },
		remove: func(ctx context.Context, cli *dockerclient.Client, img container.BunkerImage) error {
			return container.RemoveImageByTag(ctx, cli, img.Tag)
		},
	})
}
