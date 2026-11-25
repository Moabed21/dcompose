//go:build !windows

package docker

import "os"

// interrupt asks a docker CLI process to stop gracefully. The CLI forwards
// the signal to plugins such as compose.
func interrupt(p *os.Process) error {
	return p.Signal(os.Interrupt)
}
