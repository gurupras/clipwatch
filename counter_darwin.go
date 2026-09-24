package clipwatch

import (
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// macOS has no clipboard notification of any kind: NSPasteboard publishes
// changeCount, an integer that rises on every write by any program, and that is
// all Apple offers. Reading it is one message send to the pasteboard server,
// far cheaper than reading the pasteboard itself, so the watcher polls this and
// never reads content to decide whether anything changed.
const hasChangeCounter = true

var (
	appKitOnce           sync.Once
	classPasteboard      objc.Class
	classAutoreleasePool objc.Class
	selGeneralPasteboard = objc.RegisterName("generalPasteboard")
	selChangeCount       = objc.RegisterName("changeCount")
	selAlloc             = objc.RegisterName("alloc")
	selInit              = objc.RegisterName("init")
	selDrain             = objc.RegisterName("drain")
)

// A program that only watches the clipboard may not have AppKit loaded. It is
// loaded on the first poll rather than at package init, so importing this
// package costs nothing until a watcher runs.
func loadAppKit() {
	_, _ = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit",
		purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	classPasteboard = objc.GetClass("NSPasteboard")
	classAutoreleasePool = objc.GetClass("NSAutoreleasePool")
}

// changeCount returns NSPasteboard's counter, or 0 if AppKit is unavailable —
// in which case the watcher reports no changes rather than false ones.
func changeCount() uint64 {
	appKitOnce.Do(loadAppKit)
	if classPasteboard == 0 {
		return 0
	}
	// generalPasteboard hands back an autoreleased object, and this runs on a
	// timer: without a pool of its own, every poll would leak one.
	//
	// A pool belongs to the thread that created it and must be drained there,
	// on top of that thread's pool stack. Each message send below is a
	// separate call into C, and between them the scheduler may move this
	// goroutine to another thread; draining there faults inside libobjc. So
	// the thread is held for the life of the pool.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var pool objc.ID
	if classAutoreleasePool != 0 {
		pool = objc.ID(classAutoreleasePool).Send(selAlloc).Send(selInit)
		defer func() { pool.Send(selDrain) }()
	}
	pb := objc.ID(classPasteboard).Send(selGeneralPasteboard)
	if pb == 0 {
		return 0
	}
	return uint64(pb.Send(selChangeCount))
}
