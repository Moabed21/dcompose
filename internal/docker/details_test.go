package docker

import (
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
)

const inspectJSON = `{
  "Id": "0123456789abcdef0123456789abcdef",
  "Created": "2026-11-25T10:00:00.123Z",
  "Path": "nginx", "Args": ["-g", "daemon off;"],
  "State": {"Status": "exited", "OOMKilled": true, "ExitCode": 137,
    "StartedAt": "2026-11-25T10:00:01Z", "FinishedAt": "2026-11-25T11:00:00Z",
    "Health": {"Status": "unhealthy", "FailingStreak": 3,
      "Log": [{"Start": "2026-11-25T10:59:00Z", "End": "2026-11-25T10:59:01Z", "ExitCode": 1, "Output": "curl: (7) Failed to connect\n"}]}},
  "Image": "sha256:fedcba9876543210fedcba9876543210",
  "Name": "/app-web-1",
  "RestartCount": 2,
  "HostConfig": {"RestartPolicy": {"Name": "on-failure", "MaximumRetryCount": 5}, "Memory": 536870912, "NanoCpus": 1500000000},
  "Mounts": [
    {"Type": "volume", "Name": "app_data", "Source": "/var/lib/docker/volumes/app_data/_data", "Destination": "/data", "RW": true},
    {"Type": "bind", "Source": "/home/me/conf", "Destination": "/etc/conf", "RW": false}],
  "Config": {"Image": "nginx:alpine", "Env": ["Z=1", "A=2"],
    "Labels": {"com.docker.compose.project": "app", "com.docker.compose.service": "web",
      "com.docker.compose.project.config_files": "/home/me/app/compose.yaml"}},
  "NetworkSettings": {
    "Ports": {"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}, {"HostIp": "::", "HostPort": "8080"}],
      "443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8443"}], "9000/tcp": null},
    "Networks": {"app_default": {"IPAddress": "172.18.0.2", "Aliases": ["web"]}}}
}`

func TestDetailsFromInspect(t *testing.T) {
	var r container.InspectResponse
	if err := json.Unmarshal([]byte(inspectJSON), &r); err != nil {
		t.Fatal(err)
	}
	d := detailsFromInspect(r)

	check := func(name string, got, want any) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	check("name", d.Name, "app-web-1")
	check("id", d.ID, "0123456789ab")
	check("image", d.Image, "nginx:alpine")
	check("image id", d.ImageID, "fedcba987654")
	check("command", d.Command, "nginx -g daemon off;")
	check("state", d.State, "exited")
	check("exit", d.ExitCode, 137)
	check("oom", d.OOMKilled, true)
	check("restarts", d.RestartCount, 2)
	check("policy", d.RestartPolicy, "on-failure:5")
	check("memory", d.MemoryLimit, int64(512<<20))
	check("cpus", d.CPULimit, 1.5)
	check("project", d.Project, "app")
	check("service", d.Service, "web")
	check("health", d.Health, "unhealthy")
	check("streak", d.FailingStreak, 3)
	if len(d.HealthLog) != 1 || d.HealthLog[0].Output != "curl: (7) Failed to connect" {
		t.Errorf("health log: %+v", d.HealthLog)
	}
	if len(d.Ports) != 2 || d.Ports[0].HostPort != 8080 || d.Ports[1].HostPort != 8443 || d.Ports[1].HostIP != "127.0.0.1" {
		t.Errorf("ports (IPv4/IPv6 duplicates merged, unpublished skipped): %+v", d.Ports)
	}
	if len(d.Networks) != 1 || d.Networks[0].IP != "172.18.0.2" {
		t.Errorf("networks: %+v", d.Networks)
	}
	if len(d.Mounts) != 2 || d.Mounts[0].Source != "app_data" || !d.Mounts[1].ReadOnly {
		t.Errorf("mounts: %+v", d.Mounts)
	}
	if len(d.Env) != 2 || d.Env[0] != "A=2" {
		t.Errorf("env should be sorted: %v", d.Env)
	}
	if d.Created.IsZero() || d.FinishedAt.IsZero() {
		t.Error("times not parsed")
	}
}

func TestPortMappingURL(t *testing.T) {
	cases := []struct {
		p    PortMapping
		host string
		want string
	}{
		{PortMapping{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80}, "localhost", "http://localhost:8080"},
		{PortMapping{HostIP: "", HostPort: 3000, ContainerPort: 3000}, "localhost", "http://localhost:3000"},
		{PortMapping{HostIP: "::", HostPort: 8443, ContainerPort: 443}, "localhost", "https://localhost:8443"},
		{PortMapping{HostIP: "127.0.0.1", HostPort: 5000, ContainerPort: 5000}, "localhost", "http://127.0.0.1:5000"},
		{PortMapping{HostIP: "::1", HostPort: 5000, ContainerPort: 5000}, "localhost", "http://[::1]:5000"},
		{PortMapping{HostIP: "0.0.0.0", HostPort: 80, ContainerPort: 80}, "server.lan", "http://server.lan:80"},
		{PortMapping{HostIP: "127.0.0.1", HostPort: 80, ContainerPort: 80}, "server.lan", "http://server.lan:80"},
		{PortMapping{HostIP: "10.0.0.7", HostPort: 80, ContainerPort: 80}, "server.lan", "http://10.0.0.7:80"},
	}
	for _, c := range cases {
		if got := c.p.URL(c.host); got != c.want {
			t.Errorf("%+v on %s: got %s, want %s", c.p, c.host, got, c.want)
		}
	}
}

func TestPublishedPorts(t *testing.T) {
	v4, v6 := netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("::")
	got := publishedPorts([]container.PortSummary{
		{IP: v6, PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
		{IP: v4, PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
		{PrivatePort: 9000, Type: "tcp"}, // exposed, not published
		{IP: v4, PrivatePort: 53, PublicPort: 5353, Type: "udp"},
	})
	if len(got) != 2 || got[0].HostPort != 5353 || got[1].HostPort != 8080 {
		t.Fatalf("got %+v", got)
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "localhost",
		"unix:///var/run/docker.sock":    "localhost",
		"npipe:////./pipe/docker_engine": "localhost",
		"tcp://10.0.0.5:2376":            "10.0.0.5",
		"ssh://me@build-server":          "build-server",
		"ssh://me@build-server.lan:2222": "build-server.lan",
		"tcp://[2001:db8::1]:2375":       "2001:db8::1",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVolumeSizesLargestFirst(t *testing.T) {
	got := volumeSizes([]volume.Volume{
		{Name: "small", UsageData: &volume.UsageData{Size: 10, RefCount: 1}},
		{Name: "unknown"},
		{Name: "big", UsageData: &volume.UsageData{Size: 1000, RefCount: 0}},
	})
	if len(got) != 3 || got[0].Name != "big" || got[1].Name != "small" || got[2].Size != -1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].InUse || !got[1].InUse {
		t.Fatalf("in-use flags wrong: %+v", got)
	}
}
