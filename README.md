# clipwatch

Watch the system clipboard and hear about a change when it happens, rather than
a second later.

`golang.design/x/clipboard` reads and writes the clipboard on every platform,
and this package uses it for exactly that. What it adds is the watching. The
underlying library polls on Windows, macOS and X11 — a one-second ticker
comparing a change counter — so a copy waits half a second on average before
anything else learns of it. Where the platform can say when the clipboard
changed, this listens:

| Platform | How a change is noticed | Latency |
|---|---|---|
| Windows | A message-only window on the clipboard format listener list, woken by `WM_CLIPBOARDUPDATE` | immediate |
| Linux (X11) | XFixes reports a new owner of the CLIPBOARD selection | immediate |
| Linux (Wayland, wlroots/KDE) | The underlying library's watch, through the data-control protocol | immediate |
| Linux (Wayland, GNOME) | **Not supported**, see below | — |
| macOS | Polling `NSPasteboard.changeCount` — Apple publishes no notification | 100 ms–1 s, see *Hints* |

Where a backend cannot start, the watcher falls back to polling and says so
through `Mechanism()`, with the reason in `FallbackReason()`. It is never worse
than the library it wraps. `Mechanism()` is one of:

- `event`: the OS wakes the watcher on every change.
- `poll`: this package reads the platform's change counter on a timer
  (macOS, or a fallback on Windows). The only mechanism `Hint()` speeds up.
- `library`: watching is handed to `golang.design/x/clipboard` (every Wayland
  session, or Linux without a usable X11 backend). With data-control, as on
  wlroots, that is event-driven too; without it, the library reads and compares
  the content once a second.

## GNOME on Wayland is not supported

GNOME offers no data-control protocol, so nothing outside the focused window
can watch its Wayland clipboard. What remains is the X11 view through XWayland,
and GNOME updates that only once after an X11 program last owned the clipboard:
the first Wayland copy after an X11 copy is seen, and the copies after it are
not, neither as a change nor in a read, which keeps returning the first one.
x/clipboard reads through that same view on GNOME, so no watcher built on it
can do better.

`FallbackReason()` returns `ErrGNOMEWayland` there, so a program can detect it
with `errors.Is` rather than trust a watcher that will miss copies. The
clipboard that does work there is the desktop portal's
(`org.freedesktop.portal.Clipboard`, within a RemoteDesktop session).

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

Those tests copy from inside the test process. To check a copy made by another
program, set `CLIPBOARD_ONDEVICE_COPY` to a command that copies its stdin:
`xclip -selection clipboard` on X11, `clip` on Windows, `pbcopy` on macOS,
`wl-copy` on a data-control compositor. `scripts/gtk-copy.py` copies from a
focused GTK4 window, the way a user's copy happens, for desktops where no
command-line copier works (GNOME on Wayland, where it is how the limitation
above was found).

## Status

| Platform | State |
|---|---|
| Windows | Event-driven, in pure Go through `user32` |
| Linux (X11) | Event-driven, XFixes over the X11 wire protocol |
| Linux (Wayland, wlroots/KDE) | The underlying library's data-control watch; `Mechanism()` reports `library`. KDE is untested |
| Linux (Wayland, GNOME) | Not supported: consecutive Wayland copies are not seen. `FallbackReason()` is `ErrGNOMEWayland` |
| macOS | Polls `changeCount`, adaptively, because Apple offers nothing else |

The on-device tests were run on these desktops, each in a VM:

| Desktop | `Mechanism()` | This process's copy reported in | Another program's copy reported in |
|---|---|---|---|
| Windows 11 | `event` | 0–1 ms | 13 ms, mostly `clip` starting |
| Linux, GNOME on Xorg | `event` | 0–3 ms | 3 ms (`xclip`); 155–161 ms (`gtk-copy.py`, mostly GTK starting) |
| Linux, wlroots (headless sway) | `library` | 0–1 ms | 14–15 ms (`wl-copy`) |
| Linux, GNOME on Wayland | `library` | 800–850 ms | **fails**: the first copy by a Wayland app is reported, the second never is |
| macOS 15.7 (arm64) | `poll` | 800–850 ms idle; 53 ms after `Hint()`, with a 10 s idle interval | 800–850 ms (`pbcopy`) |

KDE has not been tried.

MIT licensed, like the library it builds on.
