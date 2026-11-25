package docker

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanLinesCR(t *testing.T) {
	in := "a\nb\r\nprogress 1\rprogress 2\rdone\nlast"
	sc := bufio.NewScanner(strings.NewReader(in))
	sc.Split(scanLinesCR)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	want := []string{"a", "b", "progress 1", "progress 2", "done", "last"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLineWriterSplitsAcrossWrites(t *testing.T) {
	ch := make(chan LogLine, 10)
	w := &lineWriter{ctx: context.Background(), ch: ch, stderr: true}
	_, _ = w.Write([]byte("hel"))
	_, _ = w.Write([]byte("lo\nwor"))
	_, _ = w.Write([]byte("ld\r\npartial"))
	w.flush()
	close(ch)
	var got []string
	for l := range ch {
		if !l.Stderr {
			t.Fatal("expected stderr flag")
		}
		got = append(got, l.Text)
	}
	if strings.Join(got, "|") != "hello|world|partial" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitConfigFiles(t *testing.T) {
	got := splitConfigFiles("/a/x.yml, /b/y.yml,")
	if len(got) != 2 || got[0] != "/a/x.yml" || got[1] != "/b/y.yml" {
		t.Fatalf("got %q", got)
	}
}

// Integration test against a real daemon. Run with `make it-test`, or:
//
//	DCOMPOSE_IT_DIR=$PWD/internal/docker/testdata/compose-project go test ./internal/docker -run Integration -v
//
// The project must define services: ttyapp (tty: true, prints "tty line N"),
// noisy (prints "out N" to stdout and "err N" to stderr), cache, built.
func TestIntegrationComposeLifecycle(t *testing.T) {
	dir := os.Getenv("DCOMPOSE_IT_DIR")
	if dir == "" {
		t.Skip("DCOMPOSE_IT_DIR not set")
	}
	c, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	proj, err := ResolveComposeFile(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	store := loadProjectStoreAt(filepath.Join(t.TempDir(), "projects.json"))
	store.Remember(proj)

	run := func(sub ...string) []string {
		t.Helper()
		task, err := StartDocker(ComposeArgs(proj, sub...))
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for l := range task.Lines {
			lines = append(lines, l)
		}
		if task.Err() != nil {
			t.Fatalf("compose %v failed: %v\n%s", sub, task.Err(), strings.Join(lines, "\n"))
		}
		return lines
	}

	out := run("up", "-d", "--build", "--remove-orphans")
	t.Logf("up output: %d lines, last: %q", len(out), out[len(out)-1])
	defer run("down", "--volumes", "--remove-orphans")

	projects, err := c.ListComposeProjects(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range projects {
		if p.Name == proj.Name {
			found = true
			if p.IsDown() || !p.HasConfig() {
				t.Fatalf("unexpected project state: %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("project %s not discovered", proj.Name)
	}

	svcs, err := ComposeServices(ctx, proj)
	if err != nil || len(svcs) != 4 {
		t.Fatalf("services: %v %v", svcs, err)
	}

	time.Sleep(3 * time.Second)
	containers, err := c.ListContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, ct := range containers {
		if ct.Project == proj.Name {
			ids[ct.Service] = ct.ID
		}
	}

	collect := func(s *LogStream, n int) []LogLine {
		t.Helper()
		defer s.Cancel()
		var got []LogLine
		timeout := time.After(15 * time.Second)
		for len(got) < n {
			select {
			case l, ok := <-s.Lines:
				if !ok {
					t.Fatalf("stream closed early: %v (got %v)", s.Err(), got)
				}
				got = append(got, l)
			case <-timeout:
				t.Fatalf("timeout; got %v", got)
			}
		}
		return got
	}

	// TTY container: raw stream, no multiplexing header, text intact.
	tty := collect(c.FollowContainerLogs(ids["ttyapp"], 5, ""), 3)
	for _, l := range tty {
		ts, rest, _ := strings.Cut(l.Text, " ")
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Fatalf("tty line missing timestamp: %q", l.Text)
		}
		if !strings.HasPrefix(rest, "tty line ") {
			t.Fatalf("tty line corrupted: %q", l.Text)
		}
	}

	// Non-TTY container: demultiplexed with stderr flagged.
	noisy := collect(c.FollowContainerLogs(ids["noisy"], 6, ""), 6)
	var sawOut, sawErr bool
	for _, l := range noisy {
		_, rest, _ := strings.Cut(l.Text, " ")
		switch {
		case strings.HasPrefix(rest, "out ") && !l.Stderr:
			sawOut = true
		case strings.HasPrefix(rest, "err ") && l.Stderr:
			sawErr = true
		default:
			t.Fatalf("bad line (stderr=%v): %q", l.Stderr, l.Text)
		}
	}
	if !sawOut || !sawErr {
		t.Fatalf("expected stdout and stderr lines, got %v", noisy)
	}

	// Follow really follows: new lines keep arriving after the tail.
	live := c.FollowContainerLogs(ids["ttyapp"], 0, "")
	_ = collect(live, 2)

	// Aggregated compose logs.
	pl, err := FollowComposeLogs(proj, 2)
	if err != nil {
		t.Fatal(err)
	}
	agg := collect(pl, 4)
	if !strings.Contains(agg[0].Text, "|") {
		t.Fatalf("unexpected compose log format: %q", agg[0].Text)
	}

	// Service-level operation.
	run("restart", "noisy")

	// After down, the project must still be listed (remembered) as down.
	run("down", "--remove-orphans")
	projects, err = c.ListComposeProjects(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name == proj.Name {
			if !p.IsDown() {
				t.Fatalf("expected down, got %q", p.Status)
			}
			return
		}
	}
	t.Fatalf("project disappeared after down")
}
