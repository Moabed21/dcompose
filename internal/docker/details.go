package docker

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// PortMapping is a container port published on the host.
type PortMapping struct {
	HostIP        string // "" or an unspecified address means all interfaces
	HostPort      uint16
	ContainerPort uint16
	Proto         string // tcp, udp, sctp
}

// URL returns a browser URL for the port. host is where published ports are
// reachable (see Client.BrowserHost).
func (p PortMapping) URL(host string) string {
	if ip, err := netip.ParseAddr(p.HostIP); err == nil && !ip.IsUnspecified() {
		// Bound to a specific address. A loopback binding on a remote daemon
		// is not reachable from here, so fall back to the daemon host.
		if !ip.IsLoopback() || host == "localhost" {
			host = ip.String()
		}
	}
	scheme := "http"
	if p.ContainerPort == 443 || p.ContainerPort == 8443 {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(int(p.HostPort)))
}

// publishedPorts extracts unique host-published ports (IPv4 and IPv6
// bindings of the same port are merged).
func publishedPorts(ports []container.PortSummary) []PortMapping {
	seen := map[string]bool{}
	var out []PortMapping
	for _, p := range ports {
		if p.PublicPort == 0 {
			continue
		}
		key := fmt.Sprintf("%d/%s", p.PublicPort, p.Type)
		if seen[key] {
			continue
		}
		seen[key] = true
		hostIP := ""
		if p.IP.IsValid() {
			hostIP = p.IP.String()
		}
		out = append(out, PortMapping{HostIP: hostIP, HostPort: p.PublicPort, ContainerPort: p.PrivatePort, Proto: p.Type})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostPort < out[j].HostPort })
	return out
}

// HealthCheck is one run of a container's health check.
type HealthCheck struct {
	Start    time.Time
	ExitCode int
	Output   string
}

// NetworkAttachment is a network a container is connected to.
type NetworkAttachment struct {
	Name    string
	IP      string
	Aliases []string
}

// MountInfo describes a volume or bind mount.
type MountInfo struct {
	Type        string // volume, bind, tmpfs, ...
	Source      string // volume name or host path
	Destination string
	ReadOnly    bool
}

// ContainerDetails is a readable summary of `docker inspect` for a container.
type ContainerDetails struct {
	Name, ID, Image, ImageID string
	Command                  string
	Created                  time.Time

	State      string // running, exited, ...
	StartedAt  time.Time
	FinishedAt time.Time
	ExitCode   int
	OOMKilled  bool
	Error      string

	RestartCount  int
	RestartPolicy string

	Health        string // "", starting, healthy, unhealthy
	FailingStreak int
	HealthLog     []HealthCheck // oldest first

	Project, Service, ConfigFiles string

	MemoryLimit int64   // bytes, 0 = unlimited
	CPULimit    float64 // CPUs, 0 = unlimited

	Ports    []PortMapping
	Networks []NetworkAttachment
	Mounts   []MountInfo
	Env      []string // sorted
}

// ContainerDetails fetches a readable summary of a container's configuration and state.
func (c *Client) ContainerDetails(ctx context.Context, id string) (*ContainerDetails, error) {
	res, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	return detailsFromInspect(res.Container), nil
}

func detailsFromInspect(r container.InspectResponse) *ContainerDetails {
	d := &ContainerDetails{
		Name:         strings.TrimPrefix(r.Name, "/"),
		ID:           shortID(r.ID),
		ImageID:      shortID(r.Image),
		Created:      parseTime(r.Created),
		RestartCount: r.RestartCount,
		Command:      strings.TrimSpace(r.Path + " " + strings.Join(r.Args, " ")),
	}
	if cfg := r.Config; cfg != nil {
		d.Image = cfg.Image
		d.Project = cfg.Labels["com.docker.compose.project"]
		d.Service = cfg.Labels["com.docker.compose.service"]
		d.ConfigFiles = cfg.Labels["com.docker.compose.project.config_files"]
		d.Env = append([]string(nil), cfg.Env...)
		sort.Strings(d.Env)
	}
	if s := r.State; s != nil {
		d.State = string(s.Status)
		d.StartedAt = parseTime(s.StartedAt)
		d.FinishedAt = parseTime(s.FinishedAt)
		d.ExitCode = s.ExitCode
		d.OOMKilled = s.OOMKilled
		d.Error = s.Error
		if h := s.Health; h != nil {
			d.Health = string(h.Status)
			d.FailingStreak = h.FailingStreak
			for _, l := range h.Log {
				if l != nil {
					d.HealthLog = append(d.HealthLog, HealthCheck{Start: l.Start, ExitCode: l.ExitCode, Output: strings.TrimSpace(l.Output)})
				}
			}
		}
	}
	if hc := r.HostConfig; hc != nil {
		if rp := hc.RestartPolicy; !rp.IsNone() {
			d.RestartPolicy = string(rp.Name)
			if rp.MaximumRetryCount > 0 {
				d.RestartPolicy += ":" + strconv.Itoa(rp.MaximumRetryCount)
			}
		}
		d.MemoryLimit = hc.Memory
		d.CPULimit = float64(hc.NanoCPUs) / 1e9
	}
	if ns := r.NetworkSettings; ns != nil {
		seen := map[string]bool{}
		for port, bindings := range ns.Ports {
			for _, b := range bindings {
				hp, err := strconv.ParseUint(b.HostPort, 10, 16)
				if err != nil || hp == 0 {
					continue
				}
				key := fmt.Sprintf("%d/%s", hp, port.Proto())
				if seen[key] {
					continue
				}
				seen[key] = true
				hostIP := ""
				if b.HostIP.IsValid() {
					hostIP = b.HostIP.String()
				}
				d.Ports = append(d.Ports, PortMapping{HostIP: hostIP, HostPort: uint16(hp), ContainerPort: port.Num(), Proto: string(port.Proto())})
			}
		}
		sort.Slice(d.Ports, func(i, j int) bool { return d.Ports[i].HostPort < d.Ports[j].HostPort })
		for name, ep := range ns.Networks {
			if ep == nil {
				continue
			}
			na := NetworkAttachment{Name: name, Aliases: ep.Aliases}
			if ep.IPAddress.IsValid() {
				na.IP = ep.IPAddress.String()
			}
			d.Networks = append(d.Networks, na)
		}
		sort.Slice(d.Networks, func(i, j int) bool { return d.Networks[i].Name < d.Networks[j].Name })
	}
	for _, m := range r.Mounts {
		src := m.Source
		if string(m.Type) == "volume" && m.Name != "" {
			src = m.Name
		}
		d.Mounts = append(d.Mounts, MountInfo{Type: string(m.Type), Source: src, Destination: m.Destination, ReadOnly: !m.RW})
	}
	sort.Slice(d.Mounts, func(i, j int) bool { return d.Mounts[i].Destination < d.Mounts[j].Destination })
	return d
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t
}
