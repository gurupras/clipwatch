package clipwatch

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// These tests use the machine's real clipboard: they overwrite whatever is on
// it and, on X11, take ownership of the CLIPBOARD selection. That is rude on a
// machine somebody is using, so they run only when CLIPBOARD_ONDEVICE is set,
// which is meant for a disposable virtual machine.
//
//	CLIPBOARD_ONDEVICE=1 go test -run OnDevice -v ./...
//
// What they are for is the part unit tests cannot reach: whether the platform
// backend actually receives the OS's notification, and how quickly.

func ondevice(t *testing.T) {
	t.Helper()
	if os.Getenv("CLIPBOARD_ONDEVICE") == "" {
		t.Skip("set CLIPBOARD_ONDEVICE=1 to run against this machine's clipboard (test VMs only)")
	}
	if err := Init(); err != nil {
		t.Skipf("no usable clipboard on this machine: %v", err)
	}
}

// token is a distinct value per write, so a stale clipboard cannot pass for a
// fresh copy.
func token(what string) []byte {
	return []byte(fmt.Sprintf("clipboard-ondevice-%s-%d", what, time.Now().UnixNano()))
}

// wantEvent waits for one change, and says how long it took.
func wantEvent(t *testing.T, w *Watcher, within time.Duration, what string) time.Duration {
	t.Helper()
	start := time.Now()
	select {
	case ev, ok := <-w.Events():
		if !ok {
			t.Fatalf("%s: the watcher stopped instead of reporting a change", what)
		}
		return ev.At.Sub(start)
	case <-time.After(within):
		t.Fatalf("%s: no change reported within %v", what, within)
		return 0
	}
}

// TestOnDeviceMechanism records which backend this machine got. It fails only
// where the platform is supposed to have notifications and did not get them,
// because that is the whole point of the package: a silent fall back to polling
// on Windows or X11 is a bug, not a detail.
func TestOnDeviceMechanism(t *testing.T) {
	ondevice(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Logf("%s: mechanism %s", runtime.GOOS, w.Mechanism())

	// A Wayland session skips X11 on purpose (see backend_linux.go), so only a
	// plain X11 session is expected to be event-driven on Linux.
	wantEventDriven := runtime.GOOS == "windows" ||
		(runtime.GOOS == "linux" && os.Getenv("DISPLAY") != "" && os.Getenv("WAYLAND_DISPLAY") == "")
	switch {
	case wantEventDriven && w.Mechanism() != MechanismEvent:
		t.Errorf("this platform has clipboard notifications but the watcher fell back to %s: %v", w.Mechanism(), w.FallbackReason())
	case runtime.GOOS == "darwin" && w.Mechanism() != MechanismPoll:
		t.Errorf("macOS has no clipboard notification, so polling was expected, got %s", w.Mechanism())
	}
}

// TestOnDeviceReportsACopy is the core claim: put something on the clipboard,
// and the watcher says so.
func TestOnDeviceReportsACopy(t *testing.T) {
	ondevice(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Let the backend settle before the first write, so the copy under test is
	// not lost in the setup.
	time.Sleep(200 * time.Millisecond)

	want := token("copy")
	Write(FmtText, want)
	took := wantEvent(t, w, 5*time.Second, "a copy")
	t.Logf("%s: a copy was reported in %v (%s)", runtime.GOOS, took.Round(time.Millisecond), w.Mechanism())

	// The event says a change happened; the content is there to be read.
	if got := string(Read(FmtText)); got != string(want) {
		t.Errorf("the clipboard holds %q after the event, want %q", got, want)
	}

	// An event backend must be quick. Polling is bounded by its interval, so it
	// is held to the default second plus slack rather than to the same bar.
	limit := 500 * time.Millisecond
	if w.Mechanism() == MechanismPoll {
		limit = 2 * time.Second
	}
	if took > limit {
		t.Errorf("a copy took %v to be reported, want under %v on a %s backend", took, limit, w.Mechanism())
	}
}

// TestOnDeviceSaysNothingWhileNothingIsCopied guards the opposite mistake: a
// backend that reports changes that did not happen would wake a program
// constantly and, in a remote-desktop session, send the same clipboard round
// and round.
func TestOnDeviceSaysNothingWhileNothingIsCopied(t *testing.T) {
	ondevice(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	Write(FmtText, token("quiet"))
	// Drain the change just made, then expect silence.
	select {
	case <-w.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("the write was never reported, so this test cannot tell silence from deafness")
	}
	select {
	case ev := <-w.Events():
		t.Errorf("a change was reported though nothing was copied: %+v", ev)
	case <-time.After(2 * time.Second):
	}
}

// TestOnDeviceRepeatedCopies checks that the second and later copies are
// reported too: a listener that is registered but consumed only once, or an
// X11 connection that stops reading, would pass the single-copy test.
func TestOnDeviceRepeatedCopies(t *testing.T) {
	ondevice(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 5; i++ {
		Write(FmtText, token(fmt.Sprintf("repeat-%d", i)))
		took := wantEvent(t, w, 5*time.Second, fmt.Sprintf("copy %d", i+1))
		t.Logf("copy %d reported in %v", i+1, took.Round(time.Millisecond))
		// A copy that lands while the previous event is still unread would be
		// collapsed into it, which is correct but would not prove anything, so
		// the writes are spaced.
		time.Sleep(150 * time.Millisecond)
	}
}

// TestOnDeviceHintSpeedsUpPolling is macOS's version of the promise: no
// notification exists, so a caller that knows a copy is coming asks for a
// faster interval and gets it. Elsewhere the backend is event-driven and the
// hint is a no-op, so the test only runs where polling is the mechanism.
func TestOnDeviceHintSpeedsUpPolling(t *testing.T) {
	ondevice(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{
		IdlePoll:   10 * time.Second, // slow enough that only the hint can win
		ActivePoll: 50 * time.Millisecond,
		ActiveFor:  3 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.Mechanism() != MechanismPoll {
		t.Skipf("this machine is event-driven (%s); a hint has nothing to speed up", w.Mechanism())
	}
	time.Sleep(200 * time.Millisecond)
	if err := w.Hint(); err != nil {
		t.Fatalf("Hint: %v", err)
	}
	Write(FmtText, token("hint"))
	took := wantEvent(t, w, 3*time.Second, "a hinted copy")
	t.Logf("darwin: a hinted copy was reported in %v", took.Round(time.Millisecond))
	if took > time.Second {
		t.Errorf("a hinted copy took %v, want well under the 10s idle interval", took)
	}
}
