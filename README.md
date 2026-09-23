# clipwatch

Watch the system clipboard and hear about a change when it happens, rather than
a second later.

`golang.design/x/clipboard` reads and writes the clipboard on every platform,
and this package uses it for exactly that. What it adds is the watching. The
underlying library polls — a one-second ticker comparing a change counter — so
a copy waits half a second on average before anything else learns of it. Three
of the four desktop platforms can simply say when the clipboard changed:

| Platform | How a change is noticed | Latency |
|---|---|---|
| Windows | A message-only window on the clipboard format listener list, woken by `WM_CLIPBOARDUPDATE` | immediate |
| Linux (X11) | XFixes reports a new owner of the CLIPBOARD selection | immediate |
| Linux (Wayland) | The data-control protocol, where the compositor offers it | immediate |
| macOS | Polling `NSPasteboard.changeCount` — Apple publishes no notification | 100 ms–1 s, see *Hints* |

Where a backend cannot start, the watcher falls back to polling and says so
through `Mechanism()`. It is never worse than the library it wraps.

```go
import "github.com/gurupras/clipwatch"

w, err := clipwatch.New(ctx, clipwatch.Options{})
if err != nil {
	return err
}
for range w.Events() {
	text := clipwatch.Read(clipwatch.FmtText)
	_ = text
}
```

## Events carry no content

An event says only that the clipboard changed. What was copied is read when the
caller asks for it, so a program that merely counts changes never handles
somebody's password. This package logs nothing.

## Hints, for macOS

macOS has no clipboard notification: `NSPasteboard` publishes `changeCount` and
nothing else, which is why every library on every language polls there. Polling
ten times a second to feel instant would wake the CPU ten times a second for the
life of the process.

So the watcher polls slowly — once a second by default — and a program that
knows a copy is *likely* can say so:

```go
w.Hint() // check every 100ms for the next few seconds
```

A remote-desktop host that has just delivered a Cmd+C to the machine, for
instance, knows exactly when to expect a copy. That buys event-like latency
when it matters without paying for it while idle. On the platforms with real
notifications `Hint` does nothing, because nothing is needed.

## Testing

The unit tests need no clipboard and no display: the polling logic runs against
a counter the test drives, so `go test ./...` is safe anywhere.

Whether a backend really receives the OS's notification can only be found out
on a machine with a desktop, and those tests overwrite its clipboard. They are
therefore behind an environment variable and are meant for a disposable VM:

```
scripts/ondevice.sh                          # on the machine itself
scripts/ondevice-remote.sh user@vm [GOOS]    # build here, run there
```

They check that the platform got the backend it should have (a silent fall back
to polling on Windows or X11 is a bug), that a copy is reported and how
quickly, that nothing is reported while nothing is copied, that the fifth copy
is reported as well as the first, and that a hint speeds up polling where
polling is all there is.

## Status

| Platform | State |
|---|---|
| Windows | Event-driven, in pure Go through `user32` |
| Linux (X11) | Event-driven, XFixes over the X11 wire protocol |
| Linux (Wayland) | Falls back to polling; the data-control backend is not written |
| macOS | Polls `changeCount`, adaptively, because Apple offers nothing else |

Verified by the unit tests everywhere; the on-device tests are how each backend
is confirmed on a real desktop.

MIT licensed, like the library it builds on.
