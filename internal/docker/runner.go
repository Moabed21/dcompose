package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Task is a running `docker ...` CLI command whose combined output is
// streamed line by line. Long-running operations (compose up/build/pull)
// run as tasks so they are never killed by a short timeout and the user
// sees live progress.
type Task struct {
	Args  []string
	Lines <-chan string // closed when the process has exited and output is drained

	cancel context.CancelFunc
	err    error // valid after Lines is closed
}

// Err returns the process exit error. Only valid after Lines is closed.
func (t *Task) Err() error { return t.err }

// Cancel asks the process to stop (SIGINT, then a kill after a grace period;
// on Windows the process tree is terminated right away).
func (t *Task) Cancel() { t.cancel() }

// dockerEnv is the environment for docker CLI commands. Output is never a
// terminal, so ask for plain, uncoloured, non-interactive output. These are
// environment variables rather than flags so older Compose versions that
// lack the flags still work.
func dockerEnv() []string {
	return append(os.Environ(),
		"COMPOSE_MENU=false",
		"COMPOSE_ANSI=never",
		"COMPOSE_PROGRESS=plain",
		"COMPOSE_STATUS_STDOUT=1",
		"BUILDKIT_PROGRESS=plain",
	)
}

// StartDocker starts `docker <args...>` and streams its combined output.
func StartDocker(args []string) (*Task, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnv()
	cmd.Cancel = func() error { return interrupt(cmd.Process) }
	cmd.WaitDelay = 15 * time.Second

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start docker %s: %w", strings.Join(args, " "), err)
	}

	lines := make(chan string, 4096)
	t := &Task{Args: args, Lines: lines, cancel: cancel}

	waitErr := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		waitErr <- err
	}()

	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			if line := strings.TrimRight(sc.Text(), " \t"); line != "" {
				lines <- line
			}
		}
		_, _ = io.Copy(io.Discard, pr)
		t.err = <-waitErr
		cancel()
		close(lines)
	}()

	return t, nil
}

// RunDocker runs `docker <args...>` to completion and returns stdout.
// On failure the error includes stderr.
func RunDocker(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg != "" {
			return out, fmt.Errorf("%s", msg)
		}
		return out, err
	}
	return out, nil
}

// scanLinesCR splits on \n, \r\n, or a lone \r (progress bars).
func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' {
			return i + 1, data[:i], nil
		}
		if b == '\r' {
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if atEOF {
				return i + 1, data[:i], nil
			}
			return 0, nil, nil // need more data to decide on \r\n
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
