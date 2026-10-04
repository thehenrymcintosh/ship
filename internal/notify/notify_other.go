//go:build !darwin

package notify

// Default returns the platform notifier (a no-op outside macOS in v1).
func Default() Notifier { return Nop{} }
