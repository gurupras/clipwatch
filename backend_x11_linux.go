package clipwatch

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.design/x/x11"
)

// X11 has no clipboard: it has selections, owned by whichever window last
// claimed one. The XFixes extension reports when the owner of a selection
// changes, which is exactly a copy: SelectSelectionInput on CLIPBOARD asks the
// server to send this connection an event every time somebody takes it over.
//
// The connection is this package's own, separate from whatever the reading
// library keeps, so a watcher that never reads costs one idle socket and no
// polling at all.
//
// golang.design/x/x11 frames the core requests; the XFixes ones and
// QueryExtension are framed here, following the X11 and XFixes protocol specs.

const (
	opQueryExtension  = 98
	opGetInputFocus   = 43
	xfixesQueryVer    = 0 // XFixesQueryVersion, minor opcode
	xfixesSelectInput = 2 // XFixesSelectSelectionInput, minor opcode
	ownerNotifyMask   = 1 // XFixesSetSelectionOwnerNotifyMask
	selectionNotify   = 0 // XFixesSelectionNotify, as an offset from the first event code
	xfixesMajor       = 5 // the version declared; owner notifications need only 1.0
	xfixesMinor       = 0
	setupTimeout      = 10 * time.Second
)

var le = binary.LittleEndian

type x11Watch struct {
	conn            net.Conn
	r               *bufio.Reader
	selectionNotify byte // the XFixesSelectionNotify event code on this server
}

func dialX11() (*x11Watch, x11.Setup, error) {
	d, err := x11.ParseDisplay(os.Getenv("DISPLAY"))
	if err != nil {
		return nil, x11.Setup{}, err
	}
	conn, err := net.Dial(d.Net, d.Addr)
	if err != nil {
		return nil, x11.Setup{}, err
	}
	name, data := xauth(d.Num)
	if _, err := conn.Write(x11.SetupRequest(name, data)); err != nil {
		conn.Close()
		return nil, x11.Setup{}, err
	}
	r := bufio.NewReader(conn)
	setup, err := x11.ReadSetup(r)
	if err != nil {
		conn.Close()
		return nil, x11.Setup{}, err
	}
	return &x11Watch{conn: conn, r: r}, setup, nil
}

// xauth finds this display's authorisation cookie where Xlib would look for
// it. A server that needs none — an Xvfb with -ac, or one permitting local
// connections — accepts an empty one, so a missing file is not an error here;
// a server that does need one refuses the setup, and that error is reported.
func xauth(displayNum int) (string, []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	entries, err := x11.ParseXauthority(b)
	if err != nil {
		return "", nil
	}
	host, _ := os.Hostname()
	return x11.ChooseCookie(entries, displayNum, host)
}

// request sends one request and, for those that have one, reads its reply.
// An X11 error in place of the reply is returned as an error.
func (x *x11Watch) request(b []byte) (x11.Packet, error) {
	if _, err := x.conn.Write(b); err != nil {
		return x11.Packet{}, err
	}
	return x11.NextReply(x.r)
}

// queryExtension asks for an extension's major opcode and first event code.
// present is false when the server does not have it.
func (x *x11Watch) queryExtension(name string) (major, firstEvent byte, present bool, err error) {
	b := make([]byte, 8+pad4(len(name)))
	b[0] = opQueryExtension
	le.PutUint16(b[2:], uint16(len(b)/4))
	le.PutUint16(b[4:], uint16(len(name)))
	copy(b[8:], name)
	p, err := x.request(b)
	if err != nil {
		return 0, 0, false, err
	}
	// reply: 8 = present, 9 = major opcode, 10 = first event, 11 = first error
	return p.Raw[9], p.Raw[10], p.Raw[8] == 1, nil
}

// queryVersion must precede any other XFixes request: the server refuses the
// rest until a client has declared which version it speaks.
func (x *x11Watch) queryVersion(major byte) error {
	b := make([]byte, 12)
	b[0] = major
	b[1] = xfixesQueryVer
	le.PutUint16(b[2:], uint16(len(b)/4))
	le.PutUint32(b[4:], xfixesMajor)
	le.PutUint32(b[8:], xfixesMinor)
	_, err := x.request(b)
	return err
}

// selectSelectionInput asks for an event whenever the selection changes owner.
// The request has no reply, so a refusal would otherwise surface only as an
// error packet among the events, which the event loop skips — the watcher
// would report MechanismEvent and never fire. A GetInputFocus round trip
// flushes the answer out: whatever the server says about the select arrives
// before the reply to the request that followed it.
func (x *x11Watch) selectSelectionInput(major byte, window, selection uint32) error {
	b := make([]byte, 16+4)
	b[0] = major
	b[1] = xfixesSelectInput
	le.PutUint16(b[2:], 4)
	le.PutUint32(b[4:], window)
	le.PutUint32(b[8:], selection)
	le.PutUint32(b[12:], ownerNotifyMask)
	b[16] = opGetInputFocus
	le.PutUint16(b[18:], 1)
	if _, err := x.conn.Write(b); err != nil {
		return err
	}
	for {
		p, err := x11.ReadPacket(x.r)
		switch {
		case err != nil:
			return err
		case p.IsError():
			return fmt.Errorf("the server refused XFixesSelectSelectionInput (X11 error %d)", p.ErrorCode())
		case p.IsReply():
			return nil
		}
	}
}

func pad4(n int) int { return (n + 3) &^ 3 }

func startX11(ctx context.Context, w *Watcher) error {
	x, err := setupX11()
	if err != nil {
		return fmt.Errorf("clipwatch: X11 clipboard listener: %w", err)
	}
	go x.run(ctx, w)
	return nil
}

// setupX11 connects and subscribes. Any error means the caller should fall back
// to polling; the connection is closed on the way out.
func setupX11() (_ *x11Watch, err error) {
	x, setup, err := dialX11()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			x.conn.Close()
		}
	}()
	// A server that accepts the connection and then says nothing must not
	// hang New; once subscribed, the connection only waits, for as long as
	// it takes.
	if err := x.conn.SetDeadline(time.Now().Add(setupTimeout)); err != nil {
		return nil, err
	}
	major, firstEvent, present, err := x.queryExtension("XFIXES")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("this X server has no XFIXES extension")
	}
	if err := x.queryVersion(major); err != nil {
		return nil, err
	}
	atom, err := x.request(x11.InternAtom("CLIPBOARD", false))
	if err != nil {
		return nil, err
	}
	if err := x.selectSelectionInput(major, setup.Root, atom.Atom()); err != nil {
		return nil, err
	}
	if err := x.conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	x.selectionNotify = firstEvent + selectionNotify
	return x, nil
}

// run reports every selection-owner change until the context ends or the
// connection breaks. A broken connection ends the watcher rather than leaving
// it silently deaf: Events closes, and the caller can start a new one.
func (x *x11Watch) run(ctx context.Context, w *Watcher) {
	defer close(w.events)
	defer x.conn.Close()
	stop := context.AfterFunc(ctx, func() { x.conn.Close() }) // unblocks the read
	defer stop()
	var seq uint64
	for {
		p, err := x11.NextEvent(x.r)
		if err != nil {
			return
		}
		if p.EventCode() == x.selectionNotify {
			seq++
			w.send(seq)
		}
	}
}
