package clipwatch

import (
	"context"
	"os"
)

// Linux has two display servers and they notice a copy differently. X11's
// XFixes reports a change of selection owner. Wayland deliberately hides the
// clipboard from programs that are not focused, so a watcher needs the
// data-control protocol, which wlroots-based compositors and KDE offer and
// GNOME does not; that backend is not written yet, so a Wayland session falls
// back to the underlying library's polling.
func eventBackend() (func(ctx context.Context, w *Watcher, o Options) error, bool) {
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("DISPLAY") == "" {
		return nil, false
	}
	return startX11, true
}
