package clipwatch

import (
	"context"
	"errors"
	"os"
)

// errWaylandSession is the FallbackReason in a Wayland session, so a caller can
// tell "skipped on purpose" from "X11 refused".
var errWaylandSession = errors.New("clipwatch: Wayland session: X11 notifications are not used here; deferring to the underlying library's watch")

// Linux has two display servers and they notice a copy differently. X11's
// XFixes reports a change of selection owner. Wayland hides the clipboard from
// programs that are not focused, so a watcher needs the data-control protocol,
// which wlroots-based compositors and KDE offer and GNOME does not. That
// backend is not written here yet, so a Wayland session defers to the
// underlying library's watch.
//
// XFixes is skipped in a Wayland session even when XWayland provides DISPLAY,
// as GNOME's does (DISPLAY=:0 beside WAYLAND_DISPLAY=wayland-0).
// The notification is not the problem: GNOME copies the Wayland selection into
// XWayland, so XFixes does fire. The read that follows is: on GNOME, XWayland
// does not hand the selection to an X11 client without a focused window, so an
// event would be followed by a read that returns nothing. KDE's and wlroots'
// XWayland have not been tried; FallbackReason says which path ran.
func eventBackend() startFunc {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return func(context.Context, *Watcher) error { return errWaylandSession }
	}
	return startX11
}
