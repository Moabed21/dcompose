package views

import (
	"strings"
	"testing"
	"time"

	"dcompose/internal/docker"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestSafeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"web-1":          "web-1",
		"my proj/svc":    "my_proj_svc",
		`a:b*c?"d<e>|f\`: "a_b_c__d_e__f_",
		"..":             "logs",
		"":               "logs",
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderDetails(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	d := &docker.ContainerDetails{
		Name: "app-web-1", ID: "0123456789ab", Image: "nginx:alpine", ImageID: "fedcba987654",
		Command: "nginx -g daemon off;", Created: now.Add(-2 * time.Hour),
		State: "exited", FinishedAt: now.Add(-time.Hour), ExitCode: 137, OOMKilled: true,
		RestartCount: 2, RestartPolicy: "on-failure:5", MemoryLimit: 512 << 20, CPULimit: 1.5,
		Health: "unhealthy", FailingStreak: 3,
		HealthLog: []docker.HealthCheck{{Start: now.Add(-time.Minute), ExitCode: 1, Output: "curl: (7) Failed to connect\nsecond line"}},
		Project:   "app", Service: "web", ConfigFiles: "/home/me/app/compose.yaml",
		Ports:    []docker.PortMapping{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
		Networks: []docker.NetworkAttachment{{Name: "app_default", IP: "172.18.0.2", Aliases: []string{"web"}}},
		Mounts:   []docker.MountInfo{{Type: "volume", Source: "app_data", Destination: "/data"}},
		Env:      []string{"A=2", "TOKEN=x\x1b[31my"},
	}
	out := ansi.Strip(renderDetails(d, "localhost", now))
	for _, want := range []string{
		"app-web-1", "nginx:alpine  (fedcba987654)", "exit code 137 (killed: out of memory)",
		"policy on-failure:5 · restarted 2 times", "memory 512.0M · cpus 1.5", "project app · service web",
		"unhealthy · 3 failed in a row", "curl: (7) Failed to connect", "second line",
		"0.0.0.0:8080 → 80/tcp", "http://localhost:8080", "app_default", "172.18.0.2", "aliases: web",
		"volume  app_data → /data (rw)", "Environment (2)", "A=2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("details missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(renderDetails(d, "localhost", now), "\x1b[31my") {
		t.Error("control sequences from env values must be stripped")
	}
}

func TestDiskViewAndPruneNeedsConfirmation(t *testing.T) {
	m := NewDiskModel(nil)
	m.SetSize(120, 30)
	if !strings.Contains(ansi.Strip(m.View()), "Calculating disk usage") {
		t.Fatal("expected a loading message before data arrives")
	}
	m, _ = m.Update(DiskUsageMsg{Usage: &docker.DiskUsage{
		Images:      docker.DiskUsageRow{Total: 10, Active: 4, Size: 4 << 30, Reclaimable: 1 << 30},
		Volumes:     docker.DiskUsageRow{Total: 3, Active: 1, Size: 2 << 30, Reclaimable: 1 << 30},
		BuildCache:  docker.DiskUsageRow{Total: 50, Size: 3 << 30, Reclaimable: 3 << 30},
		VolumeSizes: []docker.VolumeSize{{Name: "pgdata", Size: 1 << 30, InUse: true}, {Name: "orphan", Size: -1}},
	}})
	out := ansi.Strip(m.View())
	for _, want := range []string{"Images", "Build cache", "4.0G", "(25%)", "(100%)", "total 9.0G · reclaimable 5.0G", "Largest volumes", "pgdata", "orphan"} {
		if !strings.Contains(out, want) {
			t.Errorf("disk view missing %q:\n%s", want, out)
		}
	}

	// Select "Build cache" and press A: it must only ask for confirmation.
	for i := 0; i < 3; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg, ok := cmd().(ConfirmRequestMsg)
	if !ok || !msg.Danger || !strings.Contains(msg.Title, "build cache") {
		t.Fatalf("expected a dangerous confirmation for build cache, got %#v", msg)
	}
}

func TestOpenPortWithoutPublishedPorts(t *testing.T) {
	cmd := openPortCmd(nil, &docker.ContainerInfo{Name: "db"})
	f, ok := cmd().(FlashMsg)
	if !ok || f.Err == nil {
		t.Fatalf("expected an error flash, got %#v", f)
	}
}
