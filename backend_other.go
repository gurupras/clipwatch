//go:build !windows && !linux

package clipboard

import "context"

// No event backend: macOS has no clipboard notification (NSPasteboard offers
// changeCount and nothing else), and the other platforms this builds for are
// not desktops. The watcher polls, adaptively where a counter exists.
func eventBackend() (func(ctx context.Context, w *Watcher, o Options) error, bool) {
	return nil, false
}
