//go:build windows

package proc

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Windows support is deferred: Job objects will replace process groups.

func setGroup(cmd *exec.Cmd) {}

func terminateGroup(pid int, grace time.Duration, done <-chan struct{}) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func exitSignaled(ps *os.ProcessState) bool { return false }

// KillGroup kills the process (no groups on Windows yet).
func KillGroup(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// Alive reports whether pid exists.
func Alive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
