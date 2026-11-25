package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEditorFromEnvironment(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "code --wait")
	c, err := EditorCommand("a.yaml", "b.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Args, " "); got != "code --wait a.yaml b.yaml" {
		t.Fatalf("args: %q", got)
	}

	t.Setenv("VISUAL", "vim")
	c, err = EditorCommand("a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Args, " "); got != "vim a.yaml" {
		t.Fatalf("VISUAL should win over EDITOR: %q", got)
	}
}

func TestEditorPathWithSpaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := filepath.Join(t.TempDir(), "My Editors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ed := filepath.Join(dir, "ed it")
	if err := os.WriteFile(ed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", ed)
	c, err := EditorCommand("x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Args[0] != ed || c.Args[1] != "x.yaml" {
		t.Fatalf("args: %q", c.Args)
	}
}

func TestEditorFallback(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if runtime.GOOS == "windows" {
		c, err := EditorCommand("x.yaml")
		if err != nil || c.Args[0] != "notepad" {
			t.Fatalf("want notepad, got %v %v", c, err)
		}
		return
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := EditorCommand("x.yaml"); err == nil {
		t.Fatal("expected an error when no editor exists")
	}
	if err := os.WriteFile(filepath.Join(dir, "vi"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := EditorCommand("x.yaml")
	if err != nil || c.Args[0] != "vi" {
		t.Fatalf("want vi, got %v %v", c, err)
	}
}

func TestOpenURLUsesOpener(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		t.Skip("xdg-open is only used on Linux/BSD")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "opened")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "xdg-open"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("WSL_DISTRO_NAME", "")
	if isWSL() {
		t.Skip("running under WSL")
	}
	if err := OpenURL("http://localhost:8080"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil && string(b) == "http://localhost:8080" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("opener not called correctly: %q %v", b, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOpenURLReportsFailure(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		t.Skip("xdg-open is only used on Linux/BSD")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xdg-open"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if isWSL() {
		t.Skip("running under WSL")
	}
	err := OpenURL("http://localhost:1")
	if err == nil || !strings.Contains(err.Error(), "http://localhost:1") {
		t.Fatalf("want an error that includes the URL, got %v", err)
	}
}
