package clipwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// settle gives a new watcher time to take its baseline. A write that lands
// before a polling watcher's first read of the counter becomes that baseline
// and is never reported, so every test waits before its first write.
func settle() { time.Sleep(200 * time.Millisecond) }

// copyAndWait writes data and waits for the watcher to report it. The time is
// taken before Write: an event backend may queue its event before Write even
// returns, and a stopwatch started afterwards would read that as zero.
func copyAndWait(t *testing.T, w *Watcher, data []byte, within time.Duration, what string) time.Duration {
	t.Helper()
	start := time.Now()
	Write(FmtText, data)
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

	want := MechanismEvent
	switch {
	case runtime.GOOS == "darwin":
		want = MechanismPoll
	case runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "":
		// Skipped on purpose (see backend_linux.go), even with XWayland's DISPLAY.
		want = MechanismLibrary
	}
	if w.Mechanism() != want {
		t.Errorf("mechanism %s, want %s on this machine; fallback reason: %v", w.Mechanism(), want, w.FallbackReason())
	}
	if runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" && isGNOME() &&
		!errors.Is(w.FallbackReason(), ErrGNOMEWayland) {
		t.Errorf("GNOME Wayland must be reported as ErrGNOMEWayland, got %v", w.FallbackReason())
	}
	t.Logf("fallback reason: %v", w.FallbackReason())
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
	settle()
	want := token("copy")
	took := copyAndWait(t, w, want, 5*time.Second, "a copy")
	t.Logf("%s: a copy was reported in %v (%s)", runtime.GOOS, took.Round(time.Millisecond), w.Mechanism())

	// The event says a change happened; the content is there to be read.
	if got := string(Read(FmtText)); got != string(want) {
		t.Errorf("the clipboard holds %q after the event, want %q", got, want)
	}

	// An event backend must be quick. Polling is bounded by its interval, so it
	// is held to the default second plus slack rather than to the same bar.
	limit := 500 * time.Millisecond
	if w.Mechanism() != MechanismEvent {
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
	// First prove the watcher hears a copy, or silence would prove nothing.
	settle()
	copyAndWait(t, w, token("quiet"), 5*time.Second, "the copy before the silence")
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
	settle()
	for i := 0; i < 5; i++ {
		took := copyAndWait(t, w, token(fmt.Sprintf("repeat-%d", i)), 5*time.Second, fmt.Sprintf("copy %d", i+1))
		t.Logf("copy %d reported in %v", i+1, took.Round(time.Millisecond))
		// A copy that lands while the previous event is still unread would be
		// collapsed into it, which is correct but would not prove anything, so
		// the writes are spaced.
		time.Sleep(150 * time.Millisecond)
	}
}

// TestOnDeviceHintSpeedsUpPolling is macOS's version of the promise: no
// notification exists, so a caller that knows a copy is coming asks for a
// faster interval and gets it. It runs only where this package's own poll is
// the mechanism: an event backend has nothing to speed up, and the library's
// watch (MechanismLibrary) ignores hints, so a fast result there would be the
// library's own interval, not the hint.
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
		t.Skipf("mechanism is %s; only %s responds to a hint", w.Mechanism(), MechanismPoll)
	}
	settle()
	if err := w.Hint(); err != nil {
		t.Fatalf("Hint: %v", err)
	}
	took := copyAndWait(t, w, token("hint"), 3*time.Second, "a hinted copy")
	t.Logf("%s: a hinted copy was reported in %v", runtime.GOOS, took.Round(time.Millisecond))
	if took > time.Second {
		t.Errorf("a hinted copy took %v, want well under the 10s idle interval", took)
	}
}

// TestOnDeviceReportsACopyFromAnotherProgram is the copy that matters: one made
// by a different program, not by this process. Every other test here writes
// through this package, so this process owns the clipboard it is watching; a
// backend or a read path that only sees its own writes would pass them all.
//
// CLIPBOARD_ONDEVICE_COPY is a shell command that puts its stdin on the
// clipboard, run with the test's environment:
//
//	Linux X11        xclip -selection clipboard
//	Linux Wayland    env -u DISPLAY wl-copy   (KDE, wlroots)
//	GNOME on Xorg    GDK_BACKEND=x11 python3 scripts/gtk-copy.py
//	GNOME Wayland    not supported; the test skips (see ErrGNOMEWayland)
//	macOS            pbcopy
//	Windows          clip
func TestOnDeviceReportsACopyFromAnotherProgram(t *testing.T) {
	ondevice(t)
	copier := os.Getenv("CLIPBOARD_ONDEVICE_COPY")
	if copier == "" {
		t.Skip("set CLIPBOARD_ONDEVICE_COPY to a command that copies its stdin, e.g. wl-copy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Logf("mechanism %s, fallback reason: %v", w.Mechanism(), w.FallbackReason())
	if errors.Is(w.FallbackReason(), ErrGNOMEWayland) {
		// Measured: the first Wayland copy is seen, the second never is.
		t.Skip("GNOME Wayland is not supported: consecutive Wayland copies are not seen (ErrGNOMEWayland)")
	}
	settle()

	for i := 0; i < 3; i++ {
		want := string(token(fmt.Sprintf("external-%d", i)))
		// wl-copy and xclip leave a child behind to serve the clipboard. Given a
		// pipe, that child would hold it open and Run would wait for it, so the
		// copier's output goes to a file instead.
		logPath := filepath.Join(t.TempDir(), "copier.log")
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		// A copier that cannot copy may never exit (wl-copy on GNOME waits for
		// a selection that never lands), so it gets a deadline of its own.
		copyCtx, copyCancel := context.WithTimeout(ctx, 10*time.Second)
		cmd := shell(copyCtx, copier)
		cmd.Stdin = strings.NewReader(want)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		start := time.Now()
		err = cmd.Run()
		copyCancel()
		logFile.Close()
		if err != nil {
			out, _ := os.ReadFile(logPath)
			t.Fatalf("%q: %v\n%s", copier, err, out)
		}
		select {
		case _, ok := <-w.Events():
			if !ok {
				t.Fatal("the watcher stopped instead of reporting the copy")
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("copy %d by %q was not reported within 5s", i+1, copier)
		}
		took := time.Since(start)
		got := strings.TrimRight(string(Read(FmtText)), "\r\n")
		t.Logf("copy %d by another program: reported %v after the copier started (%s)", i+1, took.Round(time.Millisecond), w.Mechanism())
		if got != want {
			t.Errorf("copy %d: the read after the event returned %q, want %q", i+1, got, want)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func shell(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
