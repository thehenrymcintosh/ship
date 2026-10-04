//go:build darwin

package notify

import (
	"os/exec"
	"strconv"
)

type osascript struct{}

// Default returns the platform notifier.
func Default() Notifier { return osascript{} }

// Notify runs `display notification` asynchronously.
func (osascript) Notify(title, message string) {
	// strconv.Quote yields a double-quoted string AppleScript accepts for
	// plain text; it also escapes quotes and backslashes.
	script := "display notification " + strconv.Quote(truncate(message, 200)) + " with title " + strconv.Quote(truncate(title, 100))
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
