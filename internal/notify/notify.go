// Package notify sends desktop notifications. macOS uses osascript;
// other platforms are no-ops in v1.
package notify

// Notifier shows a desktop notification.
type Notifier interface {
	Notify(title, message string)
}

// Nop discards notifications.
type Nop struct{}

// Notify implements Notifier.
func (Nop) Notify(string, string) {}

// Func adapts a function.
type Func func(title, message string)

// Notify implements Notifier.
func (f Func) Notify(title, message string) { f(title, message) }
