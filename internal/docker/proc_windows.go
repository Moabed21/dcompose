//go:build windows

package docker

import (
	"os"
	"os/exec"
	"strconv"
)

// interrupt stops a docker CLI process. Windows cannot deliver SIGINT to a
// child process, and killing docker.exe alone would leave plugin processes
// (docker-compose.exe) running, so the whole process tree is terminated.
func interrupt(p *os.Process) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run(); err != nil {
		return p.Kill()
	}
	return nil
}
