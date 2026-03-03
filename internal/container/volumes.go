package container

import (
	"context"
	"strings"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/config"
)

// BunkerVolume holds information about a claude-bunker Docker volume.
type BunkerVolume struct {
	Name    string
	Kind    string // "bashhistory" or "config"
	Project string // project name portion from the container name
}

// ListBunkerVolumesDetailed returns detailed information about all claude-bunker volumes.
func ListBunkerVolumesDetailed(ctx context.Context, cli *client.Client) ([]BunkerVolume, error) {
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}

	volumePrefixes := []string{
		config.BashHistoryVolumePrefix,
		config.ClaudeConfigVolumePrefix,
	}

	var vols []BunkerVolume
	for _, v := range resp.Volumes {
		for _, prefix := range volumePrefixes {
			if strings.HasPrefix(v.Name, prefix) {
				kind := "config"
				if prefix == config.BashHistoryVolumePrefix {
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

// BunkerImage holds information about a claude-bunker Docker image.
type BunkerImage struct {
	ID   string // short image ID
	Tag  string // full tag (e.g. "claude-bunker:abc123")
	Size int64  // size in bytes
}

// ListBunkerImages returns all Docker images with the claude-bunker: prefix.
func ListBunkerImages(ctx context.Context, cli *client.Client) ([]BunkerImage, error) {
	imageRef := config.ImagePrefix + ":*"
	imagePrefix := config.ImagePrefix + ":"

	f := filters.NewArgs()
	f.Add("reference", imageRef)
	imgs, err := cli.ImageList(ctx, image.ListOptions{Filters: f})
	if err != nil {
		return nil, err
	}

	var result []BunkerImage
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if strings.HasPrefix(tag, imagePrefix) {
				shortID := img.ID
				if strings.HasPrefix(shortID, "sha256:") && len(shortID) > 19 {
					shortID = shortID[7:19]
				}
				result = append(result, BunkerImage{
					ID:   shortID,
					Tag:  tag,
					Size: img.Size,
				})
			}
		}
	}
	return result, nil
}

// RemoveImageByTag removes a Docker image by tag.
func RemoveImageByTag(ctx context.Context, cli *client.Client, tag string) error {
	_, err := cli.ImageRemove(ctx, tag, image.RemoveOptions{Force: true})
	return err
}
