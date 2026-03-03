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

	pruneVolumes(ctx, cli, force, all)
	pruneImages(ctx, cli, force, all)

	return nil
}

func pruneVolumes(ctx context.Context, cli *dockerclient.Client, force, all bool) {
	volumes, err := container.ListBunkerVolumesDetailed(ctx, cli)
	if err != nil {
		warn("Failed to list volumes: " + err.Error())
		return
	}

	if len(volumes) == 0 {
		info("No claude-bunker volumes found.")
		return
	}

	// Group volumes by project
	projects := map[string][]container.BunkerVolume{}
	var projectOrder []string
	for _, v := range volumes {
		if _, seen := projects[v.Project]; !seen {
			projectOrder = append(projectOrder, v.Project)
		}
		projects[v.Project] = append(projects[v.Project], v)
	}

	info(fmt.Sprintf("Found %d volume(s) across %d project(s).", len(volumes), len(projectOrder)))

	var selectedIndices []int

	if all || len(projectOrder) == 1 {
		// Select all projects
		for i := range projectOrder {
			selectedIndices = append(selectedIndices, i)
		}
	} else if !isTTY() {
		// Non-interactive: require --all or --force
		warn("Non-interactive terminal detected. Use --all to select all, or --force to skip prompts.")
		return
	} else {
		// Build huh options for projects
		options := make([]huh.Option[int], len(projectOrder))
		for i, project := range projectOrder {
			vols := projects[project]
			label := fmt.Sprintf("%s (%d volume(s))", project, len(vols))
			options[i] = huh.NewOption(label, i)
		}

		err := huh.NewForm(
			huh.NewGroup(
				huh.NewMultiSelect[int]().
					Title("Select projects to remove volumes from").
					Description("Space to toggle, Enter to confirm").
					Options(options...).
					Value(&selectedIndices),
			),
		).Run()

		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				info("Aborted.")
				return
			}
			warn("Selection failed: " + err.Error())
			return
		}
	}

	var toRemove []container.BunkerVolume
	for _, idx := range selectedIndices {
		toRemove = append(toRemove, projects[projectOrder[idx]]...)
	}

	if len(toRemove) == 0 {
		info("Nothing to remove.")
		return
	}

	if !force {
		if !confirmAction(fmt.Sprintf("Remove %d volume(s)?", len(toRemove))) {
			info("Aborted.")
			return
		}
	}

	removed := 0
	for _, v := range toRemove {
		if err := container.RemoveVolume(ctx, cli, v.Name); err != nil {
			warn(fmt.Sprintf("Could not remove %s (may be in use).", v.Name))
		} else {
			verbose("Removed: " + v.Name)
			removed++
		}
	}

	success(fmt.Sprintf("Pruned %d volume(s).", removed))
}

func pruneImages(ctx context.Context, cli *dockerclient.Client, force, all bool) {
	images, err := container.ListBunkerImages(ctx, cli)
	if err != nil {
		warn("Failed to list images: " + err.Error())
		return
	}

	if len(images) == 0 {
		info("No claude-bunker images found.")
		return
	}

	info(fmt.Sprintf("Found %d claude-bunker image(s).", len(images)))

	var selectedIndices []int

	if all || len(images) == 1 {
		for i := range images {
			selectedIndices = append(selectedIndices, i)
		}
	} else if !isTTY() {
		warn("Non-interactive terminal detected. Use --all to select all, or --force to skip prompts.")
		return
	} else {
		// Build huh options for images
		options := make([]huh.Option[int], len(images))
		for i, img := range images {
			sizeMB := float64(img.Size) / 1024 / 1024
			label := fmt.Sprintf("%s (%.0f MB)", img.Tag, sizeMB)
			options[i] = huh.NewOption(label, i)
		}

		err := huh.NewForm(
			huh.NewGroup(
				huh.NewMultiSelect[int]().
					Title("Select images to remove").
					Description("Space to toggle, Enter to confirm").
					Options(options...).
					Value(&selectedIndices),
			),
		).Run()

		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				info("Aborted.")
				return
			}
			warn("Selection failed: " + err.Error())
			return
		}
	}

	var toRemove []container.BunkerImage
	for _, idx := range selectedIndices {
		toRemove = append(toRemove, images[idx])
	}

	if len(toRemove) == 0 {
		info("Nothing to remove.")
		return
	}

	if !force {
		if !confirmAction(fmt.Sprintf("Remove %d image(s)?", len(toRemove))) {
			info("Aborted.")
			return
		}
	}

	removed := 0
	for _, img := range toRemove {
		if err := container.RemoveImageByTag(ctx, cli, img.Tag); err != nil {
			warn(fmt.Sprintf("Could not remove %s (may be in use).", img.Tag))
		} else {
			verbose("Removed: " + img.Tag)
			removed++
		}
	}

	success(fmt.Sprintf("Pruned %d image(s).", removed))
}
