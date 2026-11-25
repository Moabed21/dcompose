package docker

import (
	"context"
	"sort"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// DiskUsageRow summarises disk usage for one kind of object.
type DiskUsageRow struct {
	Total       int64
	Active      int64
	Size        int64
	Reclaimable int64
}

// VolumeSize is the disk usage of one volume.
type VolumeSize struct {
	Name  string
	Size  int64 // -1 if unknown
	InUse bool
}

// DiskUsage is the equivalent of `docker system df`.
type DiskUsage struct {
	Images, Containers, Volumes, BuildCache DiskUsageRow
	VolumeSizes                             []VolumeSize // largest first
}

// TotalSize is the space used by all object kinds together.
func (d DiskUsage) TotalSize() int64 {
	return d.Images.Size + d.Containers.Size + d.Volumes.Size + d.BuildCache.Size
}

// TotalReclaimable is the space prune could free across all object kinds.
func (d DiskUsage) TotalReclaimable() int64 {
	return d.Images.Reclaimable + d.Containers.Reclaimable + d.Volumes.Reclaimable + d.BuildCache.Reclaimable
}

// DiskUsage computes disk usage. This can take a few seconds on hosts with
// large volumes, since the daemon measures every volume.
func (c *Client) DiskUsage(ctx context.Context) (*DiskUsage, error) {
	res, err := c.cli.DiskUsage(ctx, client.DiskUsageOptions{
		Containers: true, Images: true, Volumes: true, BuildCache: true, Verbose: true,
	})
	if err != nil {
		return nil, err
	}
	d := &DiskUsage{
		Images:     DiskUsageRow{res.Images.TotalCount, res.Images.ActiveCount, res.Images.TotalSize, res.Images.Reclaimable},
		Containers: DiskUsageRow{res.Containers.TotalCount, res.Containers.ActiveCount, res.Containers.TotalSize, res.Containers.Reclaimable},
		Volumes:    DiskUsageRow{res.Volumes.TotalCount, res.Volumes.ActiveCount, res.Volumes.TotalSize, res.Volumes.Reclaimable},
		BuildCache: DiskUsageRow{res.BuildCache.TotalCount, res.BuildCache.ActiveCount, res.BuildCache.TotalSize, res.BuildCache.Reclaimable},
	}
	d.VolumeSizes = volumeSizes(res.Volumes.Items)
	return d, nil
}

func volumeSizes(items []volume.Volume) []VolumeSize {
	out := make([]VolumeSize, 0, len(items))
	for _, v := range items {
		vs := VolumeSize{Name: v.Name, Size: -1}
		if u := v.UsageData; u != nil {
			vs.Size = u.Size
			vs.InUse = u.RefCount > 0
		}
		out = append(out, vs)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// PruneBuildCache removes dangling build cache, or all unused build cache
// when all=true (like `docker builder prune --all`).
func (c *Client) PruneBuildCache(ctx context.Context, all bool) (int, uint64, error) {
	res, err := c.cli.BuildCachePrune(ctx, client.BuildCachePruneOptions{All: all})
	if err != nil {
		return 0, 0, err
	}
	return len(res.Report.CachesDeleted), res.Report.SpaceReclaimed, nil
}
