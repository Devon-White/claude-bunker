package container

import (
	"context"
	"strings"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// volumePrefixes are the prefixes used by claude-bunker for its Docker volumes.
var volumePrefixes = []string{
	"claude-code-bashhistory-claude-bunker-",
	"claude-code-config-claude-bunker-",
}

// BunkerVolume holds information about a claude-bunker Docker volume.
type BunkerVolume struct {
	Name    string
	Kind    string // "bashhistory" or "config"
	Project string // project name portion from the container name
}

// ListBunkerVolumes returns all Docker volumes created by claude-bunker.
// Uses exact prefix matching to avoid cross-matching.
func ListBunkerVolumes(ctx context.Context, cli *client.Client) ([]string, error) {
	vols, err := ListBunkerVolumesDetailed(ctx, cli)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(vols))
	for i, v := range vols {
		names[i] = v.Name
	}
	return names, nil
}

// ListBunkerVolumesDetailed returns detailed information about all claude-bunker volumes.
func ListBunkerVolumesDetailed(ctx context.Context, cli *client.Client) ([]BunkerVolume, error) {
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}

	var vols []BunkerVolume
	for _, v := range resp.Volumes {
		for _, prefix := range volumePrefixes {
			if strings.HasPrefix(v.Name, prefix) {
				kind := "config"
				if strings.Contains(prefix, "bashhistory") {
					kind = "bashhistory"
				}
				project := v.Name[len(prefix):]
				vols = append(vols, BunkerVolume{
					Name:    v.Name,
					Kind:    kind,
					Project: project,
				})
				break
			}
		}
	}
	return vols, nil
}

// RemoveVolume removes a Docker volume by name.
func RemoveVolume(ctx context.Context, cli *client.Client, name string) error {
	return cli.VolumeRemove(ctx, name, false)
}
