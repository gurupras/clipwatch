package clipboard

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Windows tells a window when the clipboard changes: AddClipboardFormatListener
// puts it on the system's listener list, and every change delivers
// WM_CLIPBOARDUPDATE. The window needs no pixels — HWND_MESSAGE creates one that
// never appears — but it does need a thread of its own running a message loop,
// because messages are delivered to the thread that created the window.
//
// This is what makes Windows event-driven here rather than polling
// GetClipboardSequenceNumber once a second. It also never opens the clipboard,
// so it does not contend for the desktop-wide clipboard lock.

const (
	wmClipboardUpdate = 0x031D
	wmDestroy         = 0x0002
	wmQuit            = 0x0012
	hwndMessage       = ^uintptr(2) // (HWND)-3
	cwUseDefault      = ^uintptr(0x7FFFFFFF)
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	getModuleHandleW      = kernel32.NewProc("GetModuleHandleW")
	registerClassExW      = user32.NewProc("RegisterClassExW")
	createWindowExW       = user32.NewProc("CreateWindowExW")
	destroyWindow         = user32.NewProc("DestroyWindow")
	defWindowProcW        = user32.NewProc("DefWindowProcW")
	getMessageW           = user32.NewProc("GetMessageW")
	translateMessage      = user32.NewProc("TranslateMessage")
	dispatchMessageW      = user32.NewProc("DispatchMessageW")
	postMessageW          = user32.NewProc("PostMessageW")
	addClipboardListener  = user32.NewProc("AddClipboardFormatListener")
	removeClipboardListen = user32.NewProc("RemoveClipboardFormatListener")
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

func eventBackend() (func(ctx context.Context, w *Watcher, o Options) error, bool) {
	return startWindows, true
}

func startWindows(ctx context.Context, w *Watcher, _ Options) error {
	ready := make(chan error, 1)
	go windowsLoop(ctx, w, ready)
	return <-ready
}

func windowsLoop(ctx context.Context, w *Watcher, ready chan<- error) {
	// The window belongs to the thread that made it, and only that thread
	// receives its messages.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.events)

	var seq uint64
	instance, _, _ := getModuleHandleW.Call(0)
	className, err := syscall.UTF16PtrFromString("gurupras-clipboard-watcher")
	if err != nil {
		ready <- err
		return
	}
	proc := syscall.NewCallback(func(hwnd syscall.Handle, message uint32, wParam, lParam uintptr) uintptr {
		if message == wmClipboardUpdate {
			seq++
			w.send(seq)
			return 0
		}
		r, _, _ := defWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
		return r
	})
	class := wndClassExW{
		size:      uint32(unsafe.Sizeof(wndClassExW{})),
		wndProc:   proc,
		instance:  syscall.Handle(instance),
		className: className,
	}
	// A class name already registered by an earlier watcher in this process is
	// not an error: the registration is per process and can be reused.
	if atom, _, e := registerClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		const errClassAlreadyExists = 1410
		if en, ok := e.(syscall.Errno); !ok || uintptr(en) != errClassAlreadyExists {
			ready <- fmt.Errorf("clipboard: register window class: %w", e)
			return
		}
	}
	hwnd, _, e := createWindowExW.Call(0, uintptr(unsafe.Pointer(className)), 0, 0,
		cwUseDefault, cwUseDefault, 0, 0, hwndMessage, 0, instance, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("clipboard: create message window: %w", e)
		return
	}
	defer destroyWindow.Call(hwnd)
	if ok, _, e := addClipboardListener.Call(hwnd); ok == 0 {
		ready <- fmt.Errorf("clipboard: add clipboard format listener: %w", e)
		return
	}
	defer removeClipboardListen.Call(hwnd)
	ready <- nil

	// Nothing else can end GetMessageW, so the context's end is turned into a
	// message posted to this window.
	go func() {
		<-ctx.Done()
		postMessageW.Call(hwnd, wmQuit, 0, 0)
	}()

	var m msg
	for {
		r, _, _ := getMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(r) {
		case 0, -1: // WM_QUIT, or the queue failed
			return
		}
		if m.message == wmQuit || m.message == wmDestroy {
			return
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&m)))
		dispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
