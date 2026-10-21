package backend

// Windows is only a development convenience: no process groups, kill the whole tree.

import (
	"os/exec"
	"strconv"
	"syscall"
)

func ownProcessGroup(cmd *exec.Cmd) {}

func signalGroup(pid int, _ syscall.Signal) error {
	return exec.Command("taskkill", "/pid", strconv.Itoa(pid), "/T", "/F").Run()
}
