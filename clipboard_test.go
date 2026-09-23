package clipboard

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCounter stands in for the platform's change counter so the polling
// logic can be exercised on any machine, with no clipboard and no display.
type fakeCounter struct {
	n     atomic.Uint64
	reads atomic.Int64
}

func (f *fakeCounter) read() uint64 { f.reads.Add(1); return f.n.Load() }

// waitForBaseline blocks until the watcher has read the counter once. A change
// made before that read is not a change the watcher can see, so a test that
// does not wait is testing nothing.
func (f *fakeCounter) waitForBaseline(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for f.reads.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the watcher never read the counter")
		}
		time.Sleep(time.Millisecond)
	}
}

// newTestWatcher builds a polling watcher without touching the real clipboard:
// New would call Init, which needs a display on Linux and a pasteboard on
// macOS, neither of which a test machine is owed.
func newTestWatcher(t *testing.T, ctx context.Context, f *fakeCounter, o Options) *Watcher {
	t.Helper()
	o = o.withDefaults()
	w := &Watcher{
		events:      make(chan Event, 1),
		hint:        make(chan struct{}, 1),
		done:        ctx.Done(),
		mech:        MechanismPoll,
		readCounter: f.read,
		useCounter:  true,
	}
	go w.poll(ctx, o)
	return w
}

func TestAChangeInTheCounterIsReported(t *testing.T) {
	f := &fakeCounter{}
	f.n.Store(7)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newTestWatcher(t, ctx, f, Options{IdlePoll: 10 * time.Millisecond})
	f.waitForBaseline(t)

	f.n.Store(8)
	select {
	case ev := <-w.Events():
		if ev.Seq != 1 {
			t.Errorf("first event has Seq %d, want 1", ev.Seq)
		}
		if ev.At.IsZero() {
			t.Error("the event has no time")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a change in the counter was never reported")
	}

	// A counter that has not moved is not a change, however often it is read.
	select {
	case ev := <-w.Events():
		t.Fatalf("an unchanged clipboard produced an event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// The clipboard's content is never read to decide whether it changed: only the
// counter is, which is the whole reason polling it is cheap enough to do.
func TestOnlyTheCounterIsReadWhileNothingChanges(t *testing.T) {
	f := &fakeCounter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newTestWatcher(t, ctx, f, Options{IdlePoll: 5 * time.Millisecond})

	time.Sleep(100 * time.Millisecond)
	if n := f.reads.Load(); n < 5 {
		t.Errorf("the counter was read %d times in 100ms at a 5ms interval", n)
	}
}

// A Hint makes a polling watcher check at the faster interval, which is how
// macOS gets event-like latency when a program knows a copy is coming.
func TestAHintShortensTheInterval(t *testing.T) {
	f := &fakeCounter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newTestWatcher(t, ctx, f, Options{
		IdlePoll:   30 * time.Second, // never fires during this test
		ActivePoll: 10 * time.Millisecond,
		ActiveFor:  2 * time.Second,
	})

	f.waitForBaseline(t)
	if err := w.Hint(); err != nil {
		t.Fatalf("Hint: %v", err)
	}
	f.n.Add(1)
	select {
	case <-w.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("after a Hint, a change was not noticed at the faster interval")
	}
}

// Without a Hint the idle interval stands: a watcher does not quietly poll fast
// for ever, which is what would cost a laptop its battery.
func TestWithoutAHintTheIdleIntervalStands(t *testing.T) {
	f := &fakeCounter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newTestWatcher(t, ctx, f, Options{IdlePoll: time.Second, ActivePoll: time.Millisecond})

	time.Sleep(150 * time.Millisecond)
	if n := f.reads.Load(); n > 1 {
		t.Errorf("the counter was read %d times in 150ms at a 1s idle interval", n)
	}
}

// A caller that stops reading must not stall the backend, and must see the
// newest change rather than the oldest.
func TestASlowReaderGetsTheNewestChange(t *testing.T) {
	w := &Watcher{events: make(chan Event, 1), hint: make(chan struct{}, 1)}
	for i := uint64(1); i <= 5; i++ {
		w.send(i) // nobody is reading
	}
	select {
	case ev := <-w.Events():
		if ev.Seq != 5 {
			t.Errorf("a slow reader got Seq %d, want the newest, 5", ev.Seq)
		}
	default:
		t.Fatal("no event was kept for the slow reader")
	}
}

func TestAClosedWatcherRefusesAHint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeCounter{}
	w := newTestWatcher(t, ctx, f, Options{IdlePoll: 10 * time.Millisecond})
	cancel()
	// The context's end closes Events; a Hint after that says so rather than
	// blocking or pretending.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := w.Hint(); err == ErrClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Hint never reported the watcher closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case _, ok := <-w.Events():
		if ok {
			// A pending event may still be delivered; the channel must close after.
			if _, ok := <-w.Events(); ok {
				t.Error("Events stayed open after the context ended")
			}
		}
	case <-time.After(2 * time.Second):
		t.Error("Events was never closed")
	}
}

func TestDefaultsAreFilledIn(t *testing.T) {
	o := Options{}.withDefaults()
	if o.IdlePoll != time.Second || o.ActivePoll != 100*time.Millisecond || o.ActiveFor != 3*time.Second {
		t.Errorf("defaults are %+v", o)
	}
	kept := Options{IdlePoll: time.Minute, ActivePoll: time.Second, ActiveFor: time.Hour}
	if got := kept.withDefaults(); got != kept {
		t.Errorf("withDefaults changed what the caller set: %+v", got)
	}
}
