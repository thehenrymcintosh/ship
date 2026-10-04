//go:build unix

package proc

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup sends SIGTERM to the process group, then SIGKILL after
// grace unless the process exits first.
func terminateGroup(pid int, grace time.Duration, done <-chan struct{}) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	go func() {
		select {
		case <-done:
			// The leader exited; stragglers in the group still get killed.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		case <-time.After(grace):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}()
}

func exitSignaled(ps *os.ProcessState) bool {
	if ps == nil {
		return false
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}

// KillGroup sends sig to a process group (used on daemon shutdown).
func KillGroup(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }

// Alive reports whether pid exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
