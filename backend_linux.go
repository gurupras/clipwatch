package clipwatch

import "os"

// Linux has two display servers and they notice a copy differently. X11's
// XFixes reports a change of selection owner. Wayland deliberately hides the
// clipboard from programs that are not focused, so a watcher needs the
// data-control protocol, which wlroots-based compositors and KDE offer and
// GNOME does not. That backend is not written here yet, so a Wayland session
// without XWayland defers to the underlying library's watch.
func eventBackend() startFunc {
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("DISPLAY") == "" {
		return nil
	}
	return startX11
}
