//go:build !windows && !darwin

package clipwatch

// X11 and Wayland expose no clipboard change counter: the only way to notice a
// change without an event is to ask the owner for the content and compare it.
// The fallback therefore hands over to the underlying library, which does
// exactly that, and Hint has nothing to speed up.
const hasChangeCounter = false

func changeCount() uint64 { return 0 }
