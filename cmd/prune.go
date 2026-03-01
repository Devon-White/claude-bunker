package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

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

	info(fmt.Sprintf("Found %d volume(s) across %d project(s):", len(volumes), len(projectOrder)))
	fmt.Println()
	for i, project := range projectOrder {
		vols := projects[project]
		fmt.Printf("  [%d] %s\n", i+1, project)
		for _, v := range vols {
			fmt.Printf("      %s (%s)\n", v.Name, v.Kind)
		}
	}
	fmt.Println()

	var toRemove []container.BunkerVolume

	if all || len(projectOrder) == 1 {
		toRemove = volumes
	} else {
		// Interactive selection
		fmt.Print("[claude-bunker] Enter project numbers to remove (e.g. 1,3) or 'all': ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer == "" {
			info("Aborted.")
			return
		}

		if answer == "all" {
			toRemove = volumes
		} else {
			for _, part := range strings.Split(answer, ",") {
				part = strings.TrimSpace(part)
				var idx int
				if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(projectOrder) {
					warn(fmt.Sprintf("Invalid selection: %s", part))
					continue
				}
				toRemove = append(toRemove, projects[projectOrder[idx-1]]...)
			}
		}
	}

	if len(toRemove) == 0 {
		info("Nothing to remove.")
		return
	}

	if !force {
		fmt.Printf("[claude-bunker] Remove %d volume(s)? [y/N] ", len(toRemove))
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer != "y" && answer != "yes" {
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

	info(fmt.Sprintf("Pruned %d volume(s).", removed))
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

	info(fmt.Sprintf("Found %d claude-bunker image(s):", len(images)))
	fmt.Println()
	for i, img := range images {
		sizeMB := float64(img.Size) / 1024 / 1024
		fmt.Printf("  [%d] %s (%.0f MB)\n", i+1, img.Tag, sizeMB)
	}
	fmt.Println()

	var toRemove []container.BunkerImage

	if all || len(images) == 1 {
		toRemove = images
	} else {
		fmt.Print("[claude-bunker] Enter image numbers to remove (e.g. 1,3) or 'all': ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer == "" {
			info("Aborted.")
			return
		}

		if answer == "all" {
			toRemove = images
		} else {
			for _, part := range strings.Split(answer, ",") {
				part = strings.TrimSpace(part)
				var idx int
				if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(images) {
					warn(fmt.Sprintf("Invalid selection: %s", part))
					continue
				}
				toRemove = append(toRemove, images[idx-1])
			}
		}
	}

	if len(toRemove) == 0 {
		info("Nothing to remove.")
		return
	}

	if !force {
		fmt.Printf("[claude-bunker] Remove %d image(s)? [y/N] ", len(toRemove))
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer != "y" && answer != "yes" {
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

	info(fmt.Sprintf("Pruned %d image(s).", removed))
}
