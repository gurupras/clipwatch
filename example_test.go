package clipwatch_test

import (
	"context"
	"fmt"
	"log"

	"github.com/gurupras/clipwatch"
)

func Example() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := clipwatch.New(ctx, clipwatch.Options{})
	if err != nil {
		log.Fatal(err) // no usable clipboard: no display, say
	}
	if w.Mechanism() == clipwatch.MechanismPoll && w.FallbackReason() != nil {
		log.Printf("clipboard notifications unavailable, polling: %v", w.FallbackReason())
	}

	// An event says only that something changed; read what you need.
	for range w.Events() {
		if text := clipwatch.Read(clipwatch.FmtText); text != nil {
			fmt.Printf("copied %d bytes of text\n", len(text))
		}
	}
}

// A program that knows a copy is coming — a remote-desktop service that has
// just delivered Cmd+C, say — can ask a polling watcher to look more often for
// a few seconds. On macOS, where polling is all there is, that turns up to a
// second of latency into about a tenth of one; elsewhere it costs nothing.
func ExampleWatcher_Hint() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := clipwatch.New(ctx, clipwatch.Options{})
	if err != nil {
		log.Fatal(err)
	}

	deliverCopyShortcut := func() { /* inject Cmd+C into the focused app */ }
	deliverCopyShortcut()
	_ = w.Hint()

	<-w.Events()
	fmt.Println(string(clipwatch.Read(clipwatch.FmtText)))
}
