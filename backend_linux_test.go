package clipwatch

import (
	"context"
	"errors"
	"testing"
)

func TestAWaylandSessionSkipsX11AndSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		desktop string
		want    error
	}{
		{"ubuntu:GNOME", ErrGNOMEWayland},
		{"sway", errWaylandSession},
		{"KDE", errWaylandSession},
	} {
		t.Setenv("WAYLAND_DISPLAY", "wayland-0")
		t.Setenv("DISPLAY", ":0") // XWayland: must not tempt it into X11
		t.Setenv("XDG_CURRENT_DESKTOP", tc.desktop)
		if err := eventBackend()(context.Background(), nil); !errors.Is(err, tc.want) {
			t.Errorf("%s: the backend returned %v, want %v", tc.desktop, err, tc.want)
		}
	}
}
