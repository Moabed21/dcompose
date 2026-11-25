package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestParseContextInspect(t *testing.T) {
	cases := []struct {
		name   string
		json   string
		host   string
		direct bool
	}{
		{"unix", `[{"Name":"default","Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock","SkipTLSVerify":false}},"TLSMaterial":{}}]`, "unix:///var/run/docker.sock", true},
		{"npipe", `[{"Name":"desktop-linux","Endpoints":{"docker":{"Host":"npipe:////./pipe/dockerDesktopLinuxEngine"}},"TLSMaterial":{}}]`, "npipe:////./pipe/dockerDesktopLinuxEngine", true},
		{"plain tcp", `[{"Endpoints":{"docker":{"Host":"tcp://10.0.0.5:2375"}},"TLSMaterial":{}}]`, "tcp://10.0.0.5:2375", true},
		{"tls tcp", `[{"Endpoints":{"docker":{"Host":"tcp://10.0.0.5:2376"}},"TLSMaterial":{"docker":["ca.pem","cert.pem","key.pem"]}}]`, "tcp://10.0.0.5:2376", false},
		{"ssh", `[{"Endpoints":{"docker":{"Host":"ssh://me@server"}},"TLSMaterial":{}}]`, "ssh://me@server", false},
	}
	for _, c := range cases {
		ep, err := parseContextInspect([]byte(c.json))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ep.Host != c.host || ep.direct() != c.direct {
			t.Errorf("%s: got host=%q direct=%v", c.name, ep.Host, ep.direct())
		}
	}
	if _, err := parseContextInspect([]byte(`[]`)); err == nil {
		t.Error("expected error for empty inspect output")
	}
}

func TestIsDirectScheme(t *testing.T) {
	for host, want := range map[string]bool{
		"unix:///var/run/docker.sock":    true,
		"npipe:////./pipe/docker_engine": true,
		"tcp://1.2.3.4:2376":             true,
		"ssh://user@host":                false,
	} {
		if got := isDirectScheme(host); got != want {
			t.Errorf("%s: got %v want %v", host, got, want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	sep := string(filepath.Separator)
	for in, want := range map[string]string{
		"~":                         home,
		"~/proj":                    filepath.Join(home, "proj"),
		"~" + sep + "a" + sep + "b": filepath.Join(home, "a", "b"),
		"relative":                  "relative",
		"~other/x":                  "~other/x",
	} {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "projects.json")
	cfg := filepath.Join(t.TempDir(), "compose.yaml")
	s := loadProjectStoreAt(path)
	s.Remember(ComposeProject{Name: "demo", ConfigFiles: []string{cfg}, WorkingDir: filepath.Dir(cfg)})

	got := loadProjectStoreAt(path).All()
	if len(got) != 1 || got[0].Name != "demo" || got[0].ConfigFiles[0] != cfg || got[0].WorkingDir != filepath.Dir(cfg) {
		t.Fatalf("round trip: %+v", got)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	s.Forget("demo")
	if n := len(loadProjectStoreAt(path).All()); n != 0 {
		t.Fatalf("forget: %d projects left", n)
	}
}

func TestComposeArgs(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(existing, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := ComposeProject{Name: "demo", ConfigFiles: []string{existing, filepath.Join(t.TempDir(), "gone.yaml")}}
	got := ComposeArgs(p, "up", "-d")
	want := []string{"compose", "-p", "demo", "-f", existing, "up", "-d"}
	if len(got) != len(want) {
		t.Fatalf("got %q want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

// The tests below need a running daemon; they run with the integration suite
// (make it-test DIR=...).

func TestIntegrationDialViaCLI(t *testing.T) {
	if os.Getenv("DCOMPOSE_IT_DIR") == "" {
		t.Skip("DCOMPOSE_IT_DIR not set")
	}
	// Force the CLI tunnel, as used for ssh:// and TLS contexts.
	sdk, err := client.NewClientWithOpts(append([]client.Opt{client.WithAPIVersionNegotiation()}, tunnelOpts()...)...)
	if err != nil {
		t.Fatal(err)
	}
	cli := &Client{cli: sdk, viaCLI: true}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := cli.Ping(ctx); err != nil {
		t.Fatalf("ping through docker system dial-stdio: %v", err)
	}
	ctrs, err := cli.ListContainers(ctx)
	if err != nil {
		t.Fatalf("list through docker system dial-stdio: %v", err)
	}
	// Long-lived streams over the tunnel: stats and a following log stream.
	for _, ct := range ctrs {
		if ct.State != "running" {
			continue
		}
		if _, err := cli.GetStats(ctx, ct.ID); err != nil {
			t.Fatalf("stats through tunnel: %v", err)
		}
		s := cli.FollowContainerLogs(ct.ID, 5, "")
		select {
		case _, ok := <-s.Lines:
			if !ok && s.Err() != nil {
				t.Fatalf("logs through tunnel: %v", s.Err())
			}
		case <-time.After(10 * time.Second):
			// A quiet container may print nothing; the stream being open is enough.
		}
		s.Cancel()
		break
	}
	if cli.MaxParallel() >= 16 {
		t.Fatalf("tunnelled client should limit parallelism")
	}
}

func TestIntegrationTaskCancel(t *testing.T) {
	if os.Getenv("DCOMPOSE_IT_DIR") == "" {
		t.Skip("DCOMPOSE_IT_DIR not set")
	}
	task, err := StartDocker([]string{"events"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	task.Cancel()
	done := make(chan struct{})
	go func() {
		for range task.Lines {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not stop within 10s of Cancel")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel took %s", d)
	}
}
