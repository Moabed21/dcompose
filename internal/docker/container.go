package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ListContainers returns all containers on the host.
func (c *Client) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	result, err := c.cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	out := make([]ContainerInfo, 0, len(result.Items))
	for _, ctr := range result.Items {
		out = append(out, summaryToInfo(ctr))
	}
	// Stable ordering: project, then name.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			if out[i].Project == "" || out[j].Project == "" {
				return out[i].Project != ""
			}
			return out[i].Project < out[j].Project
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// StartContainer starts a stopped container.
func (c *Client) StartContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

// StopContainer stops a running container.
func (c *Client) StopContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
	return err
}

// RestartContainer restarts a container.
func (c *Client) RestartContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
	return err
}

// KillContainer sends SIGKILL to a container.
func (c *Client) KillContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: "SIGKILL"})
	return err
}

// PauseContainer pauses a running container.
func (c *Client) PauseContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	return err
}

// UnpauseContainer resumes a paused container.
func (c *Client) UnpauseContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return err
}

// RemoveContainer force-removes a container (stopping it if needed).
func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}

// PruneContainers removes all stopped containers.
func (c *Client) PruneContainers(ctx context.Context) (int, uint64, error) {
	res, err := c.cli.ContainerPrune(ctx, client.ContainerPruneOptions{})
	if err != nil {
		return 0, 0, err
	}
	return len(res.Report.ContainersDeleted), res.Report.SpaceReclaimed, nil
}

// containerHasTTY reports whether the container was created with a TTY
// (in which case its log stream is raw, not multiplexed).
func (c *Client) containerHasTTY(ctx context.Context, id string) (bool, error) {
	res, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	return res.Container.Config != nil && res.Container.Config.Tty, nil
}

// GetStats returns a single stats snapshot for a container.
func (c *Client) GetStats(ctx context.Context, containerID string) (*ContainerStats, error) {
	result, err := c.cli.ContainerStats(ctx, containerID, client.ContainerStatsOptions{
		Stream:                false,
		IncludePreviousSample: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}
	defer result.Body.Close()

	var s container.StatsResponse
	if err := json.NewDecoder(result.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("failed to decode stats: %w", err)
	}

	stats := &ContainerStats{
		ContainerID: containerID,
		PIDs:        s.PidsStats.Current,
	}
	for _, v := range s.Networks {
		stats.NetInput += v.RxBytes
		stats.NetOutput += v.TxBytes
	}

	if s.OSType == "windows" {
		windowsStats(&s, stats)
		return stats, nil
	}

	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if systemDelta > 0 && cpuDelta > 0 {
		cpuCount := float64(s.CPUStats.OnlineCPUs)
		if cpuCount == 0 {
			cpuCount = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
		}
		if cpuCount == 0 {
			cpuCount = 1
		}
		stats.CPUPercent = (cpuDelta / systemDelta) * cpuCount * 100.0
	}

	// Memory: subtract page cache like `docker stats` does.
	usage := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < usage {
		usage -= v
	} else if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok && v < usage {
		usage -= v
	}
	stats.MemUsage = usage
	stats.MemLimit = s.MemoryStats.Limit
	if stats.MemLimit > 0 {
		stats.MemPercent = float64(stats.MemUsage) / float64(stats.MemLimit) * 100.0
	}

	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			stats.BlockRead += e.Value
		case "write":
			stats.BlockWrite += e.Value
		}
	}

	return stats, nil
}

// windowsStats fills stats for a Windows container, which reports CPU time
// in 100ns units, memory as private working set, and disk I/O separately.
func windowsStats(s *container.StatsResponse, stats *ContainerStats) {
	if !s.PreRead.IsZero() && s.Read.After(s.PreRead) && s.CPUStats.CPUUsage.TotalUsage >= s.PreCPUStats.CPUUsage.TotalUsage {
		possible := uint64(s.Read.Sub(s.PreRead).Nanoseconds()) / 100 * uint64(s.NumProcs)
		if possible > 0 {
			used := s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage
			stats.CPUPercent = float64(used) / float64(possible) * 100.0
		}
	}
	stats.MemUsage = s.MemoryStats.PrivateWorkingSet
	stats.BlockRead = s.StorageStats.ReadSizeBytes
	stats.BlockWrite = s.StorageStats.WriteSizeBytes
}

// summaryToInfo converts a container.Summary to our ContainerInfo.
func summaryToInfo(ctr container.Summary) ContainerInfo {
	name := ""
	if len(ctr.Names) > 0 {
		name = strings.TrimPrefix(ctr.Names[0], "/")
	}

	health := ""
	if ctr.Health != nil {
		health = string(ctr.Health.Status)
	}

	var vols []string
	for _, m := range ctr.Mounts {
		if string(m.Type) == "volume" && m.Name != "" {
			vols = append(vols, m.Name)
		}
	}

	return ContainerInfo{
		ID:        shortID(ctr.ID),
		FullID:    ctr.ID,
		Name:      name,
		Service:   ctr.Labels["com.docker.compose.service"],
		Project:   ctr.Labels["com.docker.compose.project"],
		Image:     ctr.Image,
		ImageID:   ctr.ImageID,
		Status:    ctr.Status,
		State:     string(ctr.State),
		Ports:     formatPorts(ctr.Ports),
		CreatedAt: time.Unix(ctr.Created, 0),
		Health:    health,
		Volumes:   vols,
	}
}

// formatPorts creates a de-duplicated, human-readable port mapping string.
func formatPorts(ports []container.PortSummary) string {
	seen := map[string]bool{}
	var parts []string
	for _, p := range ports {
		var s string
		if p.PublicPort > 0 {
			s = fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		} else {
			s = fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
		}
		if !seen[s] { // IPv4 and IPv6 bindings produce duplicates
			seen[s] = true
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
