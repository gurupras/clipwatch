package clipwatch

import "syscall"

// Windows keeps a counter that moves whenever the clipboard's contents change.
// Reading it takes no lock and opens nothing, so polling it costs a syscall —
// which is why it, and not the clipboard itself, is what the fallback reads.
const hasChangeCounter = true

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	getClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
)

func changeCount() uint64 {
	n, _, _ := getClipboardSequenceNumber.Call()
	return uint64(n)
}
