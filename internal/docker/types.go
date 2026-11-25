package docker

import "time"

// ContainerInfo holds display-friendly container information.
type ContainerInfo struct {
	ID        string // short (12 char) ID
	FullID    string
	Name      string
	Service   string // compose service name (from label)
	Project   string // compose project name (from label)
	Image     string
	ImageID   string
	Status    string // e.g. "Up 2 hours", "Exited (0) 5 minutes ago"
	State     string // running, exited, restarting, paused, created, dead
	Ports     string
	Published []PortMapping // ports published on the host
	CreatedAt time.Time
	Health    string   // healthy, unhealthy, starting, ""
	Volumes   []string // named volumes mounted
	Pending   string   // UI-only: in-flight operation verb ("stopping", …)
}

// ContainerStats holds real-time resource usage for a container.
type ContainerStats struct {
	ContainerID   string
	ContainerName string
	CPUPercent    float64
	MemUsage      uint64 // bytes
	MemLimit      uint64 // bytes
	MemPercent    float64
	NetInput      uint64
	NetOutput     uint64
	BlockRead     uint64
	BlockWrite    uint64
	PIDs          uint64
}

// NetworkEndpoint represents a container attached to a network.
type NetworkEndpoint struct {
	Name string
	IPv4 string
	Mac  string
}

// NetworkInfo describes a docker network.
type NetworkInfo struct {
	ID         string
	Name       string
	Driver     string
	Scope      string
	Subnet     string
	Gateway    string
	Project    string
	Internal   bool
	Containers []string          // container names attached
	Endpoints  []NetworkEndpoint // container endpoint details (IP, MAC)
}

// IsBuiltin reports whether the network is one of Docker's predefined networks.
func (n NetworkInfo) IsBuiltin() bool {
	return n.Name == "bridge" || n.Name == "host" || n.Name == "none"
}

// VolumeInfo describes a docker volume.
type VolumeInfo struct {
	Name       string
	Driver     string
	MountPoint string
	CreatedAt  string
	Project    string
	UsedBy     []string // container names using it
}

// ImageInfo describes a local docker image.
type ImageInfo struct {
	ID         string // short ID
	FullID     string
	Repo       string
	Tag        string
	Size       int64
	CreatedAt  time.Time
	Containers int // containers (any state) using this image
}

// Ref returns a reference usable with docker commands (repo:tag, or ID for dangling images).
func (i ImageInfo) Ref() string {
	if i.Repo == "" || i.Repo == "<none>" {
		return i.ID
	}
	return i.Repo + ":" + i.Tag
}

// Dangling reports whether the image has no tag.
func (i ImageInfo) Dangling() bool {
	return i.Repo == "" || i.Repo == "<none>"
}

// ComposeProject represents a compose project, either discovered from the
// daemon or remembered from a previous session.
type ComposeProject struct {
	Name        string
	Status      string   // e.g. "running(2), exited(1)" or "down"
	ConfigFiles []string // absolute paths
	WorkingDir  string
}

// IsDown reports whether the project has no containers at all.
func (p ComposeProject) IsDown() bool {
	return p.Status == "" || p.Status == "down"
}
