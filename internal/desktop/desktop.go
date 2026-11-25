// Package desktop opens URLs in the user's browser and files in their
// editor, on Linux (including WSL), macOS and Windows.
package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// OpenURL opens u in the default web browser.
func OpenURL(u string) error {
	name, args, err := openerFor(u)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	// Keep the browser's output out of the TUI and detach it from the
	// terminal so it survives dcompose exiting.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not open a browser (%v); open %s yourself", err, u)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Openers like xdg-open usually hand off and exit at once; report a
	// quick failure, but don't wait on one that keeps running.
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s failed (%v); open %s yourself", name, err, u)
		}
	case <-time.After(2 * time.Second):
	}
	return nil
}

func openerFor(u string) (string, []string, error) {
	switch runtime.GOOS {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", u}, nil
	case "darwin":
		return "open", []string{u}, nil
	}
	if isWSL() {
		if _, err := exec.LookPath("wslview"); err == nil {
			return "wslview", []string{u}, nil
		}
		if _, err := exec.LookPath("cmd.exe"); err == nil {
			return "cmd.exe", []string{"/c", "start", "", u}, nil
		}
	}
	for _, name := range []string{"xdg-open", "sensible-browser", "x-www-browser"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, []string{u}, nil
		}
	}
	return "", nil, fmt.Errorf("no way to open a browser found (install xdg-utils); open %s yourself", u)
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// EditorCommand returns a command that edits files with $VISUAL or $EDITOR,
// falling back to a sensible editor for the platform.
func EditorCommand(files ...string) (*exec.Cmd, error) {
	name, args, err := editor()
	if err != nil {
		return nil, err
	}
	return exec.Command(name, append(args, files...)...), nil
}

func editor() (string, []string, error) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		v := strings.TrimSpace(os.Getenv(env))
		if v == "" {
			continue
		}
		// A full path that contains spaces (e.g. under "Program Files").
		if _, err := exec.LookPath(v); err == nil {
			return v, nil, nil
		}
		// Otherwise an editor plus arguments, e.g. "code --wait".
		f := strings.Fields(v)
		return f[0], f[1:], nil
	}
	if runtime.GOOS == "windows" {
		return "notepad", nil, nil
	}
	for _, name := range []string{"nano", "vim", "vi"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, nil, nil
		}
	}
	return "", nil, fmt.Errorf("no editor found; set the EDITOR environment variable")
}
