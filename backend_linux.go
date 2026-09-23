package clipboard

import "context"

// The X11 and Wayland backends land next; until then Linux polls through the
// underlying library, as it did before this package existed.
func eventBackend() (func(ctx context.Context, w *Watcher, o Options) error, bool) {
	return nil, false
}
