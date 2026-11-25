package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// composeListEntry matches the JSON output of `docker compose ls --format json`.
type composeListEntry struct {
	Name        string `json:"Name"`
	Status      string `json:"Status"`
	ConfigFiles string `json:"ConfigFiles"`
}

// ComposeArgs builds `docker compose` arguments for a project. Config files
// are only passed if they still exist on disk; otherwise the project is
// addressed by name alone, which is enough for ps/stop/start/restart/down/logs.
func ComposeArgs(p ComposeProject, sub ...string) []string {
	args := []string{"compose"}
	if p.Name != "" {
		args = append(args, "-p", p.Name)
	}
	for _, f := range p.ConfigFiles {
		if fileExists(f) {
			args = append(args, "-f", f)
		}
	}
	return append(args, sub...)
}

// HasConfig reports whether at least one of the project's compose files exists.
func (p ComposeProject) HasConfig() bool {
	for _, f := range p.ConfigFiles {
		if fileExists(f) {
			return true
		}
	}
	return false
}

// ListComposeProjects returns every compose project known to the daemon
// (running or stopped), merged with projects remembered from earlier sessions
// so that a project that was brought `down` can be brought `up` again.
func (c *Client) ListComposeProjects(ctx context.Context, store *ProjectStore) ([]ComposeProject, error) {
	out, err := RunDocker(ctx, "compose", "ls", "--all", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("docker compose ls: %w", err)
	}

	var entries []composeListEntry
	if len(strings.TrimSpace(string(out))) > 0 {
		if err := json.Unmarshal(out, &entries); err != nil {
			return nil, fmt.Errorf("failed to parse compose ls output: %w", err)
		}
	}

	byName := map[string]ComposeProject{}
	for _, e := range entries {
		files := splitConfigFiles(e.ConfigFiles)
		p := ComposeProject{
			Name:        e.Name,
			Status:      e.Status,
			ConfigFiles: files,
		}
		if len(files) > 0 {
			p.WorkingDir = filepath.Dir(files[0])
		}
		byName[p.Name] = p
		if store != nil && len(files) > 0 {
			store.Remember(p)
		}
	}

	if store != nil {
		for _, k := range store.All() {
			if _, ok := byName[k.Name]; !ok {
				k.Status = "down"
				byName[k.Name] = k
			}
		}
	}

	projects := make([]ComposeProject, 0, len(byName))
	for _, p := range byName {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

// ComposeServices returns the service names defined in a project's config.
func ComposeServices(ctx context.Context, p ComposeProject) ([]string, error) {
	if !p.HasConfig() {
		return nil, nil
	}
	out, err := RunDocker(ctx, ComposeArgs(p, "config", "--services")...)
	if err != nil {
		return nil, err
	}
	var services []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			services = append(services, l)
		}
	}
	sort.Strings(services)
	return services, nil
}

// ComposeConfig returns the fully resolved compose configuration.
func ComposeConfig(ctx context.Context, p ComposeProject) (string, error) {
	if !p.HasConfig() {
		return "", fmt.Errorf("no compose file found on disk for project %q (looked for: %s)",
			p.Name, strings.Join(p.ConfigFiles, ", "))
	}
	out, err := RunDocker(ctx, ComposeArgs(p, "config")...)
	return string(out), err
}

// ExistingConfigFiles returns the project's compose files that exist on disk.
func (p ComposeProject) ExistingConfigFiles() []string {
	var out []string
	for _, f := range p.ConfigFiles {
		if fileExists(f) {
			out = append(out, f)
		}
	}
	return out
}

// ValidateCompose checks that the project's compose files parse and resolve.
func ValidateCompose(ctx context.Context, p ComposeProject) error {
	_, err := RunDocker(ctx, ComposeArgs(p, "config", "--quiet")...)
	return err
}

// ResolveComposeFile turns a path (file or directory) into a ComposeProject
// by asking compose for the project name.
func ResolveComposeFile(ctx context.Context, path string) (ComposeProject, error) {
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return ComposeProject{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return ComposeProject{}, err
	}
	file := abs
	if st.IsDir() {
		file = FindComposeFile(abs)
		if file == "" {
			return ComposeProject{}, fmt.Errorf("no compose file found in %s", abs)
		}
	}

	out, err := RunDocker(ctx, "compose", "-f", file, "config", "--format", "json")
	if err != nil {
		return ComposeProject{}, err
	}
	var cfg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil || cfg.Name == "" {
		return ComposeProject{}, fmt.Errorf("could not determine project name for %s", file)
	}
	return ComposeProject{
		Name:        cfg.Name,
		Status:      "down",
		ConfigFiles: []string{file},
		WorkingDir:  filepath.Dir(file),
	}, nil
}

// FindComposeFile returns the default compose file in dir, if any.
func FindComposeFile(dir string) string {
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		p := filepath.Join(dir, n)
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func splitConfigFiles(s string) []string {
	var files []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		return p
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(h, p[1:])
}

// ---------------------------------------------------------------------------
// Project store: remembers compose projects across sessions.

// ProjectStore persists known compose projects to the user config dir.
type ProjectStore struct {
	mu       sync.Mutex
	path     string
	projects map[string]ComposeProject
}

type storedProject struct {
	Name        string   `json:"name"`
	ConfigFiles []string `json:"config_files"`
}

// ProjectStorePath returns where known projects are saved
// (e.g. ~/.config/dcompose/projects.json on Linux,
// ~/Library/Application Support/dcompose/projects.json on macOS,
// %AppData%\dcompose\projects.json on Windows), or "" if there is no
// user config directory.
func ProjectStorePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "dcompose", "projects.json")
}

// LoadProjectStore loads (or initialises) the store at ProjectStorePath.
func LoadProjectStore() *ProjectStore {
	return loadProjectStoreAt(ProjectStorePath())
}

func loadProjectStoreAt(path string) *ProjectStore {
	s := &ProjectStore{projects: map[string]ComposeProject{}, path: path}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var list []storedProject
	if json.Unmarshal(data, &list) == nil {
		for _, sp := range list {
			if sp.Name == "" || len(sp.ConfigFiles) == 0 {
				continue
			}
			s.projects[sp.Name] = ComposeProject{
				Name:        sp.Name,
				ConfigFiles: sp.ConfigFiles,
				WorkingDir:  filepath.Dir(sp.ConfigFiles[0]),
			}
		}
	}
	return s
}

// Remember adds or updates a project. Persists only when something changed.
func (s *ProjectStore) Remember(p ComposeProject) {
	if p.Name == "" || len(p.ConfigFiles) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.projects[p.Name]; ok && strings.Join(old.ConfigFiles, ",") == strings.Join(p.ConfigFiles, ",") {
		return
	}
	s.projects[p.Name] = ComposeProject{Name: p.Name, ConfigFiles: p.ConfigFiles, WorkingDir: p.WorkingDir}
	s.saveLocked()
}

// Forget removes a project from the store.
func (s *ProjectStore) Forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[name]; ok {
		delete(s.projects, name)
		s.saveLocked()
	}
}

// All returns all remembered projects.
func (s *ProjectStore) All() []ComposeProject {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ComposeProject, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	return out
}

func (s *ProjectStore) saveLocked() {
	if s.path == "" {
		return
	}
	list := make([]storedProject, 0, len(s.projects))
	for _, p := range s.projects {
		list = append(list, storedProject{Name: p.Name, ConfigFiles: p.ConfigFiles})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(s.path), 0o755) != nil {
		return
	}
	// Write then rename so a crash never leaves a truncated file behind.
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil {
		_ = os.Remove(tmp)
		return
	}
	if os.Rename(tmp, s.path) != nil {
		_ = os.Remove(tmp)
	}
}
