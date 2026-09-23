//go:build !windows && !linux

package clipwatch

// No event backend: macOS has no clipboard notification (NSPasteboard offers
// changeCount and nothing else), and the other platforms this builds for are
// not desktops. The watcher polls, adaptively where a counter exists.
func eventBackend() startFunc { return nil }
