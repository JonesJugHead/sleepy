//go:build !windows

package backend

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup: the container's Ctrl+C (sent to Sleepy's group) does not reach Java directly,
// Sleepy stops it cleanly itself; and signalGroup can reach Java through sh.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup signals sh and everything it started (Java included).
func signalGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
