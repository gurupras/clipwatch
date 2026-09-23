package clipwatch

import (
	"os"
	"strings"
)

// isGNOME reports whether this is a GNOME session. XDG_CURRENT_DESKTOP is a
// colon-separated list, "ubuntu:GNOME" on Ubuntu. Only the Linux backend
// acts on it; it is here, untagged, so the on-device tests can ask too.
func isGNOME() bool {
	for _, d := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if strings.EqualFold(d, "GNOME") {
			return true
		}
	}
	return false
}
