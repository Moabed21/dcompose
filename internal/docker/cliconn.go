package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// dialViaCLI opens a connection to the daemon by running
// `docker system dial-stdio`, which proxies its stdin/stdout to whatever
// endpoint the CLI is configured for (ssh, TLS, ...).
func dialViaCLI(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Deliberately not bound to ctx: the HTTP transport pools connections
	// beyond the lifetime of the request that dialed them.
	cmd := exec.Command("docker", "system", "dial-stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// A plain os.Pipe (unlike StdoutPipe) is not closed by Wait, so output
	// still buffered when the process exits is never lost.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = outW
	c := &cliConn{cmd: cmd, stdin: stdin, stdout: outR, done: make(chan struct{})}
	cmd.Stderr = &c.stderr
	err = cmd.Start()
	outW.Close() // the child holds its own copy
	if err != nil {
		outR.Close()
		return nil, fmt.Errorf("docker system dial-stdio: %w", err)
	}
	go func() {
		_ = cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

// cliConn is a net.Conn over the stdio of a `docker system dial-stdio` process.
type cliConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *os.File
	stderr syncBuffer
	done   chan struct{} // closed once the process has exited and stderr is drained
	once   sync.Once
}

func (c *cliConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if err == io.EOF {
		// The CLI exited; if it failed (e.g. ssh could not connect), its
		// stderr explains why, which is far more useful than a bare EOF.
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
		}
		if msg := strings.TrimSpace(c.stderr.String()); msg != "" {
			return n, fmt.Errorf("docker system dial-stdio: %s", msg)
		}
	}
	return n, err
}

func (c *cliConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *cliConn) CloseWrite() error { return c.stdin.Close() }

func (c *cliConn) Close() error {
	c.once.Do(func() {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.stdin.Close()
		_ = c.stdout.Close() // also unblocks a pending Read
	})
	return nil
}

func (c *cliConn) LocalAddr() net.Addr                { return cliAddr{} }
func (c *cliConn) RemoteAddr() net.Addr               { return cliAddr{} }
func (c *cliConn) SetDeadline(t time.Time) error      { return nil }
func (c *cliConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *cliConn) SetWriteDeadline(t time.Time) error { return nil }

type cliAddr struct{}

func (cliAddr) Network() string { return "docker-cli" }
func (cliAddr) String() string  { return "docker system dial-stdio" }

// syncBuffer collects a bounded amount of stderr output safely.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := 8192 - len(b.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
