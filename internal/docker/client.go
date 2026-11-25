package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// Client wraps the Docker SDK client.
type Client struct {
	cli    *client.Client
	viaCLI bool // connections are tunnelled through `docker system dial-stdio`
}

// NewClient creates a Docker client that talks to the same daemon as the
// docker CLI. DOCKER_HOST (and the DOCKER_TLS_* variables) win if set;
// otherwise the CLI's current context is used, so Docker Desktop, Colima,
// rootless Docker, and remote contexts all work without extra setup.
// Endpoints the SDK can't reach on its own (ssh://, or TCP with TLS
// material stored in a context) are tunnelled through the docker CLI.
func NewClient() (*Client, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	viaCLI := false

	if host := os.Getenv(client.EnvOverrideHost); host != "" {
		// With TLS verification on but no DOCKER_CERT_PATH, the CLI falls back
		// to the certificates in ~/.docker, which the SDK does not do.
		tlsWithoutCerts := os.Getenv(client.EnvTLSVerify) != "" && os.Getenv(client.EnvOverrideCertPath) == ""
		viaCLI = !isDirectScheme(host) || tlsWithoutCerts
	} else if ep, err := currentContextEndpoint(); err == nil && ep.Host != "" {
		if ep.direct() {
			opts = append(opts, client.WithHost(ep.Host))
		} else {
			viaCLI = true
		}
	}
	if viaCLI {
		opts = append(opts, tunnelOpts()...)
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &Client{cli: cli, viaCLI: viaCLI}, nil
}

// tunnelOpts route every connection through dialViaCLI. The placeholder
// unix host only selects a transport without proxy or DNS handling.
func tunnelOpts() []client.Opt {
	return []client.Opt{client.WithHost("unix:///dcompose-via-docker-cli.sock"), client.WithDialContext(dialViaCLI)}
}

// Ping checks if the Docker daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.cli.Ping(ctx, client.PingOptions{})
	if err != nil {
		// Tunnelled requests go to a placeholder URL; show only the cause.
		var ue *url.Error
		if c.viaCLI && errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("docker daemon unreachable: %w", err)
	}
	return nil
}

// Close closes the underlying Docker client.
func (c *Client) Close() error {
	return c.cli.Close()
}

// MaxParallel is how many API requests should run concurrently. Tunnelled
// connections each spawn a CLI process (and possibly an ssh session), so
// they are kept to a handful.
func (c *Client) MaxParallel() int {
	if c.viaCLI {
		return 4
	}
	return 16
}

// contextEndpoint is the docker endpoint of a CLI context.
type contextEndpoint struct {
	Host          string
	SkipTLSVerify bool
	hasTLS        bool
}

// direct reports whether the SDK can connect to the endpoint by itself.
func (e contextEndpoint) direct() bool {
	switch {
	case strings.HasPrefix(e.Host, "unix://"), strings.HasPrefix(e.Host, "npipe://"):
		return true
	case strings.HasPrefix(e.Host, "tcp://"):
		return !e.hasTLS && !e.SkipTLSVerify
	}
	return false
}

func isDirectScheme(host string) bool {
	for _, s := range []string{"unix://", "npipe://", "tcp://", "http://", "https://"} {
		if strings.HasPrefix(host, s) {
			return true
		}
	}
	return false
}

// currentContextEndpoint asks the docker CLI for the endpoint of its current
// context (honouring DOCKER_CONTEXT and ~/.docker/config.json).
func currentContextEndpoint() (contextEndpoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := RunDocker(ctx, "context", "inspect")
	if err != nil {
		return contextEndpoint{}, err
	}
	return parseContextInspect(out)
}

func parseContextInspect(out []byte) (contextEndpoint, error) {
	var list []struct {
		Endpoints map[string]struct {
			Host          string
			SkipTLSVerify bool
		}
		TLSMaterial map[string][]string
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return contextEndpoint{}, err
	}
	if len(list) == 0 {
		return contextEndpoint{}, fmt.Errorf("docker context inspect returned nothing")
	}
	ep := list[0].Endpoints["docker"]
	return contextEndpoint{
		Host:          ep.Host,
		SkipTLSVerify: ep.SkipTLSVerify,
		hasTLS:        len(list[0].TLSMaterial["docker"]) > 0,
	}, nil
}
