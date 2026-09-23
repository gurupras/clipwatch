package clipwatch

import (
	"context"
	"errors"
	"os"
)

// errWaylandSession is the FallbackReason in a Wayland session other than
// GNOME, so a caller can tell "skipped on purpose" from "X11 refused".
var errWaylandSession = errors.New("clipwatch: Wayland session: X11 notifications are not used here; deferring to the underlying library's watch")

// Linux has two display servers and they notice a copy differently. X11's
// XFixes reports a change of selection owner. Wayland hides the clipboard from
// programs that are not focused, so a watcher needs the data-control protocol,
// which wlroots-based compositors and KDE offer and GNOME does not. That
// backend is not written here; a Wayland session defers to the underlying
// library's watch, which uses data-control where the compositor offers it.
//
// XFixes is not used in a Wayland session, even with XWayland's DISPLAY set,
// because XWayland does not keep up with the Wayland selection. On GNOME, a
// Wayland copy reaches XWayland only once after XWayland last owned the
// clipboard: the first native Wayland copy after an X11 copy is seen, and the
// ones after it are not, neither as an XFixes event nor in a read, which
// returns the stale first copy. Without data-control, x/clipboard reads through
// XWayland too, so GNOME Wayland cannot be watched reliably at all from here;
// see ErrGNOMEWayland.
func eventBackend() startFunc {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		reason := errWaylandSession
		if isGNOME() {
			reason = ErrGNOMEWayland
		}
		return func(context.Context, *Watcher) error { return reason }
	}
	return startX11
}
