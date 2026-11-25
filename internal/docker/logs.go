package docker

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strconv"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// LogLine is a single line of container output.
type LogLine struct {
	Text   string
	Stderr bool
}

// LogStream is a live, following log stream.
type LogStream struct {
	Lines <-chan LogLine // closed when the stream ends

	cancel func()
	mu     sync.Mutex
	err    error
}

// Cancel stops the stream.
func (s *LogStream) Cancel() { s.cancel() }

// Err returns the error that ended the stream (nil on normal EOF or cancel).
func (s *LogStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *LogStream) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// FollowContainerLogs streams a container's logs (with timestamps), following
// new output. tail is the number of history lines; since (RFC3339Nano, may be
// empty) restricts history to lines after that time.
func (c *Client) FollowContainerLogs(id string, tail int, since string) *LogStream {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan LogLine, 4096)
	s := &LogStream{Lines: ch, cancel: cancel}

	go func() {
		defer close(ch)

		tty, err := c.containerHasTTY(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				s.setErr(err)
			}
			return
		}

		opts := client.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Timestamps: true,
			Tail:       tailArg(tail),
			Since:      since,
		}
		rc, err := c.cli.ContainerLogs(ctx, id, opts)
		if err != nil {
			if ctx.Err() == nil {
				s.setErr(err)
			}
			return
		}
		defer rc.Close()
		go func() { <-ctx.Done(); rc.Close() }()

		stdout := &lineWriter{ctx: ctx, ch: ch}
		stderr := &lineWriter{ctx: ctx, ch: ch, stderr: true}
		if tty {
			_, err = io.Copy(stdout, rc)
		} else {
			_, err = stdcopy.StdCopy(stdout, stderr, rc)
		}
		stdout.flush()
		stderr.flush()
		if err != nil && ctx.Err() == nil && err != io.EOF {
			s.setErr(err)
		}
	}()
	return s
}

func tailArg(n int) string {
	if n < 0 {
		return "all"
	}
	return strconv.Itoa(n)
}

// FollowComposeLogs streams aggregated logs for a whole compose project.
func FollowComposeLogs(p ComposeProject, tail int) (*LogStream, error) {
	args := ComposeArgs(p, "logs", "--follow", "--no-color", "--tail", strconv.Itoa(tail))
	task, err := StartDocker(args)
	if err != nil {
		return nil, err
	}
	ch := make(chan LogLine, 4096)
	s := &LogStream{Lines: ch, cancel: task.Cancel}
	go func() {
		defer close(ch)
		for l := range task.Lines {
			ch <- LogLine{Text: l}
		}
		if err := task.Err(); err != nil {
			s.setErr(err)
		}
	}()
	return s, nil
}

// lineWriter splits written bytes into lines and sends them on a channel.
type lineWriter struct {
	ctx    context.Context
	ch     chan<- LogLine
	stderr bool
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(bytes.TrimRight(w.buf[:i], "\r"))
		w.buf = w.buf[i+1:]
		select {
		case w.ch <- LogLine{Text: line, Stderr: w.stderr}:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	// Protect against a single enormous line without newline.
	if len(w.buf) > 1024*1024 {
		w.flush()
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) == 0 {
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(w.buf))
	sc.Buffer(make([]byte, 0, len(w.buf)+1), len(w.buf)+1)
	for sc.Scan() {
		select {
		case w.ch <- LogLine{Text: sc.Text(), Stderr: w.stderr}:
		case <-w.ctx.Done():
			return
		}
	}
	w.buf = nil
}
