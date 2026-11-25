//go:build unix

package desktop

import (
	"os/exec"
	"syscall"
)

// detach starts the process in its own session, so closing the terminal
// does not take the browser down with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
