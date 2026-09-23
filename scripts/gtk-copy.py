#!/usr/bin/env python3
"""Copy stdin to the clipboard the way a user's app does: from a focused GTK4
window. For the on-device tests on GNOME Wayland, where wl-copy cannot copy
(GNOME has no data-control) and a Wayland client may set the clipboard only
while it has keyboard focus.

    CLIPBOARD_ONDEVICE_COPY='env -u DISPLAY GDK_BACKEND=wayland python3 scripts/gtk-copy.py'

The parent returns once the copy is made. A detached child keeps serving the
clipboard for SERVE_SECONDS, because on Wayland the content goes away when
the program that copied it exits. Needs python3-gi and GTK 4's introspection
data (gir1.2-gtk-4.0 on Debian and Ubuntu).
"""
import os
import sys

SERVE_SECONDS = 30
READY_TIMEOUT = 10


def serve(text, ready_fd):
    import gi
    gi.require_version("Gtk", "4.0")
    gi.require_version("Gdk", "4.0")
    from gi.repository import Gdk, GLib, Gtk

    app = Gtk.Application(application_id="dev.clipwatch.copier")

    def on_activate(app):
        win = Gtk.ApplicationWindow(application=app, title="clipwatch copier")
        copied = False

        def on_active(win, _param):
            nonlocal copied
            if copied or not win.is_active():
                return
            copied = True
            provider = Gdk.ContentProvider.new_for_bytes(
                "text/plain;charset=utf-8", GLib.Bytes.new(text.encode()))
            win.get_clipboard().set_content(provider)
            os.write(ready_fd, b"ok")
            os.close(ready_fd)
            GLib.timeout_add_seconds(SERVE_SECONDS, app.quit)

        win.connect("notify::is-active", on_active)
        win.present()

    app.connect("activate", on_activate)
    app.run([])


def main():
    text = sys.stdin.read()
    r, w = os.pipe()
    if os.fork() == 0:
        os.close(r)
        os.setsid()
        null = os.open(os.devnull, os.O_RDWR)
        for fd in (0, 1, 2):
            os.dup2(null, fd)
        serve(text, w)
        os._exit(0)
    os.close(w)
    import select
    ready, _, _ = select.select([r], [], [], READY_TIMEOUT)
    if not ready or os.read(r, 2) != b"ok":
        sys.exit("gtk-copy: the window never got focus, so nothing was copied")


if __name__ == "__main__":
    main()
