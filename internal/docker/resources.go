package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// ---------------------------------------------------------------- networks

// ListNetworks returns all Docker networks with container attachment info.
func (c *Client) ListNetworks(ctx context.Context) ([]NetworkInfo, error) {
	result, err := c.cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	out := make([]NetworkInfo, 0, len(result.Items))
	for _, n := range result.Items {
		info := NetworkInfo{
			ID:       shortID(n.ID),
			Name:     n.Name,
			Driver:   n.Driver,
			Scope:    n.Scope,
			Internal: n.Internal,
			Project:  n.Labels["com.docker.compose.project"],
		}
		if len(n.IPAM.Config) > 0 {
			info.Subnet = n.IPAM.Config[0].Subnet.String()
			info.Gateway = n.IPAM.Config[0].Gateway.String()
		}

		insp, err := c.cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err == nil {
			for _, ep := range insp.Network.Containers {
				info.Containers = append(info.Containers, ep.Name)
				info.Endpoints = append(info.Endpoints, NetworkEndpoint{
					Name: ep.Name,
					IPv4: ep.IPv4Address.String(),
					Mac:  ep.MacAddress.String(),
				})
			}
			sort.Slice(info.Endpoints, func(i, j int) bool { return info.Endpoints[i].Name < info.Endpoints[j].Name })
			sort.Strings(info.Containers)
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RemoveNetwork removes a network.
func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	_, err := c.cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	return err
}

// PruneNetworks removes all unused networks.
func (c *Client) PruneNetworks(ctx context.Context) (int, error) {
	res, err := c.cli.NetworkPrune(ctx, client.NetworkPruneOptions{})
	if err != nil {
		return 0, err
	}
	return len(res.Report.NetworksDeleted), nil
}

// ----------------------------------------------------------------- volumes

// ListVolumes returns all Docker volumes. containers is used to compute usage.
func (c *Client) ListVolumes(ctx context.Context, containers []ContainerInfo) ([]VolumeInfo, error) {
	resp, err := c.cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	usedBy := map[string][]string{}
	for _, ctr := range containers {
		for _, v := range ctr.Volumes {
			usedBy[v] = append(usedBy[v], ctr.Name)
		}
	}

	result := make([]VolumeInfo, 0, len(resp.Items))
	for _, v := range resp.Items {
		result = append(result, VolumeInfo{
			Name:       v.Name,
			Driver:     v.Driver,
			MountPoint: v.Mountpoint,
			CreatedAt:  formatCreated(v.CreatedAt),
			Project:    v.Labels["com.docker.compose.project"],
			UsedBy:     usedBy[v.Name],
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Project != result[j].Project {
			if result[i].Project == "" || result[j].Project == "" {
				return result[i].Project != ""
			}
			return result[i].Project < result[j].Project
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// RemoveVolume removes a volume.
func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	_, err := c.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: force})
	return err
}

// PruneVolumes removes unused volumes. all=false only removes anonymous ones.
func (c *Client) PruneVolumes(ctx context.Context, all bool) (int, uint64, error) {
	res, err := c.cli.VolumePrune(ctx, client.VolumePruneOptions{All: all})
	if err != nil {
		return 0, 0, err
	}
	return len(res.Report.VolumesDeleted), res.Report.SpaceReclaimed, nil
}

// ------------------------------------------------------------------ images

// ListImages returns local images (one row per tag). containers is used to
// compute how many containers use each image.
func (c *Client) ListImages(ctx context.Context, containers []ContainerInfo) ([]ImageInfo, error) {
	res, err := c.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	usage := map[string]int{}
	for _, ctr := range containers {
		usage[ctr.ImageID]++
	}

	var out []ImageInfo
	for _, img := range res.Items {
		base := ImageInfo{
			ID:         shortID(img.ID),
			FullID:     img.ID,
			Size:       img.Size,
			CreatedAt:  time.Unix(img.Created, 0),
			Containers: usage[img.ID],
		}
		if len(img.RepoTags) == 0 {
			base.Repo, base.Tag = "<none>", "<none>"
			if len(img.RepoDigests) > 0 {
				base.Repo = strings.SplitN(img.RepoDigests[0], "@", 2)[0]
			}
			out = append(out, base)
			continue
		}
		for _, rt := range img.RepoTags {
			i := base
			if idx := strings.LastIndex(rt, ":"); idx > 0 && !strings.Contains(rt[idx:], "/") {
				i.Repo, i.Tag = rt[:idx], rt[idx+1:]
			} else {
				i.Repo, i.Tag = rt, "latest"
			}
			out = append(out, i)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dangling() != out[j].Dangling() {
			return !out[i].Dangling()
		}
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

// RemoveImage removes an image by reference (repo:tag or ID).
func (c *Client) RemoveImage(ctx context.Context, ref string, force bool) error {
	_, err := c.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: force, PruneChildren: true})
	return err
}

// PruneImages removes dangling images (or all unused images if all=true).
func (c *Client) PruneImages(ctx context.Context, all bool) (int, uint64, error) {
	f := make(client.Filters)
	if all {
		f = f.Add("dangling", "false")
	} else {
		f = f.Add("dangling", "true")
	}
	res, err := c.cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: f})
	if err != nil {
		return 0, 0, err
	}
	return len(res.Report.ImagesDeleted), res.Report.SpaceReclaimed, nil
}

func formatCreated(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}
