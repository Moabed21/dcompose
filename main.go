package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = ""

const usage = `dcompose — a terminal UI for Docker and Docker Compose

Usage:
  dcompose [compose-file-or-directory ...]
  dcompose --help | --version

Paths given as arguments (and a compose file in the current directory) are
registered as compose projects, so they can be brought up even when they
have no containers yet. Known projects are remembered in:
  %s

dcompose connects to the same Docker daemon as the docker CLI (DOCKER_HOST,
DOCKER_CONTEXT, or the current "docker context").

Press ? inside the app for keybindings.
`

func main() {
	var paths []string
	onlyPaths := false
	for _, a := range os.Args[1:] {
		if onlyPaths || !strings.HasPrefix(a, "-") {
			paths = append(paths, a)
			continue
		}
		switch a {
		case "-h", "--help":
			fmt.Printf(usage, storeLocation())
			return
		case "-v", "--version":
			fmt.Println("dcompose", appVersion())
			return
		case "--":
			onlyPaths = true
		default:
			fmt.Fprintf(os.Stderr, "dcompose: unknown option %q (see dcompose --help)\n", a)
			os.Exit(2)
		}
	}

	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "Error: the docker CLI was not found in PATH (needed for compose, exec, and pulls).")
		fmt.Fprintln(os.Stderr, "Install Docker Desktop or Docker Engine: https://docs.docker.com/get-docker/")
		os.Exit(1)
	}

	client, err := docker.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\nMake sure Docker is running and accessible.\n", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n%s\n", err, daemonHint())
		os.Exit(1)
	}

	store := docker.LoadProjectStore()

	// Register projects from arguments, or from the current directory.
	if len(paths) == 0 {
		if wd, err := os.Getwd(); err == nil && docker.FindComposeFile(wd) != "" {
			paths = append(paths, wd)
		}
	}
	for _, p := range paths {
		rctx, rcancel := context.WithTimeout(context.Background(), 20*time.Second)
		proj, err := docker.ResolveComposeFile(rctx, p)
		rcancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", p, err)
			continue
		}
		store.Remember(proj)
	}

	p := tea.NewProgram(tui.NewModel(client, store), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}

func appVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func storeLocation() string {
	if p := docker.ProjectStorePath(); p != "" {
		return p
	}
	return "(nowhere: no user config directory found)"
}

func daemonHint() string {
	hint := "Cannot connect to the Docker daemon. Check that `docker info` works in this terminal."
	switch runtime.GOOS {
	case "darwin", "windows":
		return hint + "\nIs Docker Desktop (or your Docker VM) running?"
	default:
		return hint + "\nIs the daemon running (sudo systemctl start docker), and can your user access it\n" +
			"(member of the docker group, or using a rootless/Desktop context)?"
	}
}
