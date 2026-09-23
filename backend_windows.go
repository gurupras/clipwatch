package clipwatch

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// Windows tells a window when the clipboard changes: AddClipboardFormatListener
// puts it on the system's listener list, and every change delivers
// WM_CLIPBOARDUPDATE. The window needs no pixels — HWND_MESSAGE creates one that
// never appears — but it does need a thread of its own running a message loop,
// because messages are delivered only to the thread that created the window.
//
// This never opens the clipboard, so it does not contend for the desktop-wide
// clipboard lock the way reading it would.

const (
	wmClipboardUpdate     = 0x031D
	wmQuit                = 0x0012
	hwndMessage           = ^uintptr(2) // (HWND)-3
	errClassAlreadyExists = 1410
	windowClassName       = "gurupras-clipwatch"
)

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	getModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
	registerClassExW              = user32.NewProc("RegisterClassExW")
	createWindowExW               = user32.NewProc("CreateWindowExW")
	destroyWindow                 = user32.NewProc("DestroyWindow")
	defWindowProcW                = user32.NewProc("DefWindowProcW")
	getMessageW                   = user32.NewProc("GetMessageW")
	dispatchMessageW              = user32.NewProc("DispatchMessageW")
	postMessageW                  = user32.NewProc("PostMessageW")
	addClipboardFormatListener    = user32.NewProc("AddClipboardFormatListener")
	removeClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
)

type wndClassExW struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   syscall.Handle
	icon       syscall.Handle
	cursor     syscall.Handle
	background syscall.Handle
	menuName   *uint16
	className  *uint16
	iconSm     syscall.Handle
}

type msg struct {
	hwnd     syscall.Handle
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       struct{ x, y int32 }
	lPrivate uint32
}

// One window class, and one window procedure, serve every watcher in the
// process. A class is registered once per process and keeps the procedure it
// was registered with, and syscall.NewCallback slots are never freed, so a
// procedure per watcher would both leak and deliver every watcher's messages to
// the first one. The procedure finds its watcher by window handle instead.
var (
	classOnce sync.Once
	className *uint16
	classErr  error
	instance  uintptr

	listenersMu sync.Mutex
	listeners   = map[uintptr]*windowsListener{}
)

// windowsListener is one watcher's window. seq is touched only by the window
// procedure, which runs on the window's own thread.
type windowsListener struct {
	w   *Watcher
	seq uint64
}

func registerClass() error {
	classOnce.Do(func() {
		instance, _, _ = getModuleHandleW.Call(0)
		className, classErr = syscall.UTF16PtrFromString(windowClassName)
		if classErr != nil {
			return
		}
		class := wndClassExW{
			size:      uint32(unsafe.Sizeof(wndClassExW{})),
			wndProc:   syscall.NewCallback(wndProc),
			instance:  syscall.Handle(instance),
			className: className,
		}
		// Another copy of this package in the process (a vendored one, say)
		// may have registered the name first; its procedure would not know our
		// windows, so that is an error rather than something to share.
		if atom, _, e := registerClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
			classErr = fmt.Errorf("register window class: %w", e)
		}
	})
	return classErr
}

func wndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if message == wmClipboardUpdate {
		listenersMu.Lock()
		l := listeners[hwnd]
		listenersMu.Unlock()
		if l != nil {
			l.seq++
			l.w.send(l.seq)
		}
		return 0
	}
	r, _, _ := defWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func eventBackend() startFunc { return startWindows }

func startWindows(ctx context.Context, w *Watcher) error {
	ready := make(chan error, 1)
	go runWindowsListener(ctx, w, ready)
	if err := <-ready; err != nil {
		return fmt.Errorf("clipwatch: Windows clipboard listener: %w", err)
	}
	return nil
}

// runWindowsListener creates the window, reports on ready whether that worked,
// and then runs the window's message loop until ctx ends.
func runWindowsListener(ctx context.Context, w *Watcher, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := registerClass(); err != nil {
		ready <- err
		return
	}
	hwnd, _, e := createWindowExW.Call(0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0, hwndMessage, 0, instance, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("create message window: %w", e)
		return
	}
	defer destroyWindow.Call(hwnd)

	listenersMu.Lock()
	listeners[hwnd] = &windowsListener{w: w}
	listenersMu.Unlock()
	defer func() {
		listenersMu.Lock()
		delete(listeners, hwnd)
		listenersMu.Unlock()
	}()

	if ok, _, e := addClipboardFormatListener.Call(hwnd); ok == 0 {
		ready <- fmt.Errorf("add clipboard format listener: %w", e)
		return
	}
	defer removeClipboardFormatListener.Call(hwnd)

	// From here the watcher is ours, and Events closes when this loop ends.
	// Before here a failure leaves it untouched for the polling fallback.
	defer close(w.events)
	ready <- nil

	// GetMessageW blocks until a message arrives, so the context's end is
	// delivered as one: WM_QUIT makes GetMessageW return 0.
	stop := context.AfterFunc(ctx, func() { postMessageW.Call(hwnd, wmQuit, 0, 0) })
	defer stop()

	var m msg
	for {
		r, _, _ := getMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 is WM_QUIT, -1 a failure
			return
		}
		dispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
