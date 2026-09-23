// Package clipwatch watches the system clipboard for changes and tells you the
// moment one happens, instead of asking once a second whether anything has.
//
// Reading and writing are golang.design/x/clipboard's, unchanged: this package
// adds only the watching. That library polls on Windows, macOS and X11 — a
// one-second ticker comparing a change counter — so a copy waits half a second
// on average and a second at worst before the other side hears about it. Where
// the platform can say when the clipboard changed, this listens instead:
//
//	Windows         a message-only window on the clipboard format listener
//	                list, woken by WM_CLIPBOARDUPDATE
//	Linux (X11)     XFixes, which reports a new owner of the CLIPBOARD selection
//	Linux (Wayland) the underlying library's watch, through data-control where
//	                the compositor offers it (wlroots, KDE); GNOME has none
//	                and is not supported, see ErrGNOMEWayland
//	macOS           nothing: NSPasteboard offers changeCount and no
//	                notification, so this polls, but adapts (see
//	                Options.ActivePoll)
//
// Where a backend cannot start — an X server without XFixes, a Wayland
// session, a locked-down Windows session — the watcher falls back and says so
// through Watcher.Mechanism, with the reason in Watcher.FallbackReason. It is
// therefore never worse than the library it wraps.
//
// Events carry no clipboard content. What was copied is read only when the
// caller asks, through Read, so a program that only wants to know "something
// changed" never touches the data. Nothing here logs.
package clipwatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	xclip "golang.design/x/clipboard"
)

// Format is the kind of content on the clipboard, re-exported so callers need
// not import the underlying library for the common case.
type Format = xclip.Format

const (
	// FmtText is UTF-8 text.
	FmtText = xclip.FmtText
	// FmtImage is a PNG image.
	FmtImage = xclip.FmtImage
)

// Mechanism is how a Watcher learns about changes, for callers that want to
// report it (and for tests that must know which path ran).
type Mechanism string

const (
	// MechanismEvent means the OS wakes us when the clipboard changes.
	MechanismEvent Mechanism = "event"
	// MechanismPoll means this package reads the platform's change counter on
	// a timer, because the OS offers no notification (macOS) or the event
	// backend could not start. Only this mechanism responds to Hint.
	MechanismPoll Mechanism = "poll"
	// MechanismLibrary means the platform has no change counter either, so
	// watching is handed to golang.design/x/clipboard's own watch: event-driven
	// on a Wayland compositor with data-control, a one-second content
	// comparison otherwise. IdlePoll, ActivePoll and Hint have no effect on it.
	MechanismLibrary Mechanism = "library"
)

// Event reports that the clipboard changed. It deliberately carries no data:
// read the content with Read if you want it, so a watcher that only counts
// changes never handles what somebody copied.
type Event struct {
	// At is when the change was observed.
	At time.Time
	// Seq counts events from this watcher, starting at 1. A caller that falls
	// behind can tell how many it missed, because Events drops rather than
	// blocks the backend.
	Seq uint64
}

// Options configure a Watcher. The zero value is sensible everywhere.
type Options struct {
	// IdlePoll is how often a polling backend reads the change counter when
	// nothing suggests a copy is imminent. Zero means one second, which is what
	// golang.design/x/clipboard uses. Ignored by an event backend.
	IdlePoll time.Duration

	// ActivePoll is the faster interval a polling backend uses for ActiveFor
	// after Hint is called. Zero means 100ms. On macOS, where no notification
	// exists, a program that knows when a copy is likely — a remote-desktop
	// host that just delivered a Ctrl/Cmd+C, say — can call Hint and get
	// event-like latency without paying for it while idle. Ignored by an event
	// backend, which needs no hints.
	ActivePoll time.Duration

	// ActiveFor is how long a Hint keeps the faster interval. Zero means 3s.
	ActiveFor time.Duration

	// ForcePoll skips the event backend. For tests, and for a caller who has
	// reason to distrust the platform's notifications.
	ForcePoll bool
}

func (o Options) withDefaults() Options {
	if o.IdlePoll <= 0 {
		o.IdlePoll = time.Second
	}
	if o.ActivePoll <= 0 {
		o.ActivePoll = 100 * time.Millisecond
	}
	if o.ActiveFor <= 0 {
		o.ActiveFor = 3 * time.Second
	}
	return o
}

// ErrClosed is returned by Hint on a watcher whose context has ended.
var ErrClosed = errors.New("clipwatch: the watcher is closed")

// ErrGNOMEWayland is the FallbackReason in a GNOME Wayland session, where
// clipboard changes cannot be watched reliably: GNOME has no data-control, and
// the X11 view of the clipboard that remains shows only the first Wayland copy
// after an X11 one. The watcher still runs, but it misses copies and a Read
// may return an earlier copy. A program that needs the clipboard there must
// use the desktop portal's clipboard (org.freedesktop.portal.Clipboard, in a
// RemoteDesktop session). Test for it with errors.Is.
var ErrGNOMEWayland = errors.New("clipwatch: GNOME Wayland: the clipboard cannot be watched reliably here; consecutive Wayland copies are not seen, and reads may be stale; use the desktop portal")

// Init prepares the underlying clipboard library. It is not required — New
// calls it — but a caller that wants to fail early, before any goroutine
// starts, can call it itself. It is safe to call more than once.
func Init() error { return xclip.Init() }

// Read returns the current clipboard content in the given format, or nil if the
// clipboard holds nothing of that kind.
func Read(f Format) []byte { return xclip.Read(f) }

// Write puts content on the clipboard. The returned channel fires when some
// other program takes the clipboard over.
func Write(f Format, data []byte) <-chan struct{} { return xclip.Write(f, data) }

// Watcher reports clipboard changes until its context ends.
type Watcher struct {
	events   chan Event
	hint     chan struct{}
	mech     Mechanism
	fallback error
	done     <-chan struct{}

	// The platform's change counter, per watcher rather than package-wide so a
	// test can drive the polling logic on any machine without racing another
	// test's watcher. useCounter is false where the platform has none.
	readCounter func() uint64
	useCounter  bool
}

// startFunc starts an event backend. On success the backend owns w.events and
// closes it when it stops; on failure it must not have touched w.events,
// because the polling fallback takes it over.
type startFunc func(ctx context.Context, w *Watcher) error

// New starts watching. The watcher stops when ctx ends, and Events is closed
// after the last event, so a range over it terminates.
//
// New does not fail because a platform lacks notifications: it falls back to
// polling and reports that through Mechanism. It returns an error only when the
// clipboard itself is unusable — no display on Linux, say — because then
// neither watching nor reading can work.
func New(ctx context.Context, o Options) (*Watcher, error) {
	if err := xclip.Init(); err != nil {
		return nil, fmt.Errorf("clipwatch: the clipboard is unavailable: %w", err)
	}
	return newWatcher(ctx, o, eventBackend(), changeCount, hasChangeCounter), nil
}

// newWatcher is New without the platform: tests pass their own backend and
// counter, so every path through it runs on any machine.
func newWatcher(ctx context.Context, o Options, start startFunc, counter func() uint64, useCounter bool) *Watcher {
	o = o.withDefaults()
	w := &Watcher{
		// One in flight is enough: an event says only "something changed", so a
		// caller that is behind wants the newest, not a queue of duplicates.
		events:      make(chan Event, 1),
		hint:        make(chan struct{}, 1),
		mech:        MechanismPoll,
		done:        ctx.Done(),
		readCounter: counter,
		useCounter:  useCounter,
	}
	if start != nil && !o.ForcePoll {
		err := start(ctx, w)
		if err == nil {
			w.mech = MechanismEvent
			return w
		}
		// Not an error the caller can act on: polling still works. It is kept
		// for FallbackReason, because this package does not log.
		w.fallback = err
	}
	if !useCounter {
		w.mech = MechanismLibrary
	}
	go w.poll(ctx, o)
	return w
}

// Events yields one value per clipboard change. It is closed when the watcher
// stops. A slow reader misses intermediate changes rather than delaying the
// backend; Event.Seq says how many.
func (w *Watcher) Events() <-chan Event { return w.events }

// Mechanism says whether this watcher is woken by the OS or polls.
func (w *Watcher) Mechanism() Mechanism { return w.mech }

// FallbackReason says why the platform's event backend could not start — no
// XFIXES on this X server, say — so a caller can report why it is polling
// where it expected events. It is nil when the event backend is running, when
// the platform has none (macOS), and when Options.ForcePoll was set.
func (w *Watcher) FallbackReason() error { return w.fallback }

// Hint tells a polling watcher that a copy is likely about to happen, so it
// checks more often for Options.ActiveFor. It does nothing unless Mechanism is
// MechanismPoll: an event backend needs no help, and the library's watch has
// no interval to shorten. It never blocks.
func (w *Watcher) Hint() error {
	select {
	case <-w.done:
		return ErrClosed
	default:
	}
	select {
	case w.hint <- struct{}{}:
	default: // already hinted; the burst is extended by the poll loop
	}
	return nil
}

// send delivers an event, replacing an undelivered one rather than blocking the
// backend: the newest change is the one worth having.
func (w *Watcher) send(seq uint64) {
	ev := Event{At: time.Now(), Seq: seq}
	select {
	case w.events <- ev:
		return
	default:
	}
	select {
	case <-w.events:
	default:
	}
	select {
	case w.events <- ev:
	default:
	}
}

// poll is the fallback, and the only backend on macOS: read the platform's
// change counter — an integer, not the clipboard's content — and report when it
// moves. It runs at IdlePoll until Hint, then at ActivePoll for ActiveFor.
//
// Where the platform has no counter (X11 and Wayland expose none), it hands
// over to the underlying library's own watch; see MechanismLibrary.
func (w *Watcher) poll(ctx context.Context, o Options) {
	if !w.useCounter {
		w.watchViaLibrary(ctx)
		return
	}
	defer close(w.events)
	last := w.readCounter()
	var seq uint64
	var burstUntil time.Time // zero while idle
	t := time.NewTimer(o.IdlePoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.hint:
			// Only the first hint of a burst moves the timer. Resetting it on
			// every hint would let a caller that hints more often than
			// ActivePoll postpone the next check indefinitely.
			if burstUntil.IsZero() {
				t.Reset(o.ActivePoll)
			}
			burstUntil = time.Now().Add(o.ActiveFor)
		case now := <-t.C:
			if cur := w.readCounter(); cur != last {
				last = cur
				seq++
				w.send(seq)
			}
			if !burstUntil.IsZero() && now.After(burstUntil) {
				burstUntil = time.Time{}
			}
			if burstUntil.IsZero() {
				t.Reset(o.IdlePoll)
			} else {
				t.Reset(o.ActivePoll)
			}
		}
	}
}

// watchViaLibrary forwards golang.design/x/clipboard's own watch, for platforms
// with no change counter to poll. It watches text and images both, because a
// caller asking "did the clipboard change" means either.
func (w *Watcher) watchViaLibrary(ctx context.Context) {
	defer close(w.events)
	var seq uint64
	for range xclip.Watch(ctx) {
		seq++
		w.send(seq)
	}
}
