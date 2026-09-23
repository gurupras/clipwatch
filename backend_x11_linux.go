package clipboard

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/user"
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

const (
	opQueryExtension  = 98
	xfixesQueryVer    = 0 // XFixesQueryVersion, minor opcode
	xfixesSelectInput = 2 // XFixesSelectSelectionInput, minor opcode
	ownerNotifyMask   = 1 // XFixesSetSelectionOwnerNotifyMask
	selectionNotify   = 0 // XFixesSelectionNotify, event offset from firstEvent
	xfixesMajor       = 5 // the version this asks for; owner notifications are 1.0
	xfixesMinor       = 0 //
	replyTimeout      = 10 * time.Second
)

var le = binary.LittleEndian

// x11Watch is a connection that reports selection-owner changes.
type x11Watch struct {
	conn  net.Conn
	r     *bufio.Reader
	first byte // XFixes' first event code, from QueryExtension
}

// dialX11 opens and authorises a connection to the display in $DISPLAY.
func dialX11() (*x11Watch, x11.Setup, error) {
	d, err := x11.ParseDisplay(os.Getenv("DISPLAY"))
	if err != nil {
		return nil, x11.Setup{}, err
	}
	conn, err := net.Dial(d.Net, d.Addr)
	if err != nil {
		return nil, x11.Setup{}, fmt.Errorf("clipboard: connect to the X server: %w", err)
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

// xauth finds this display's authorisation cookie. A server that needs none —
// an Xvfb with -ac, or a host permitting local connections — accepts an empty
// one, so a missing file is not an error here.
func xauth(displayNum int) (string, []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		if u, err := user.Current(); err == nil {
			path = filepath.Join(u.HomeDir, ".Xauthority")
		}
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

// queryExtension asks for an extension's opcodes. present is false when the
// server does not have it, which is not an error: the caller falls back.
func (x *x11Watch) queryExtension(name string) (major, firstEvent byte, present bool, err error) {
	b := make([]byte, 8+pad4(len(name)))
	b[0] = opQueryExtension
	le.PutUint16(b[2:], uint16(len(b)/4))
	le.PutUint16(b[4:], uint16(len(name)))
	copy(b[8:], name)
	if _, err := x.conn.Write(b); err != nil {
		return 0, 0, false, err
	}
	p, err := x11.NextReply(x.r)
	if err != nil {
		return 0, 0, false, err
	}
	if p.IsError() {
		return 0, 0, false, fmt.Errorf("clipboard: QueryExtension(%s) failed with error %d", name, p.ErrorCode())
	}
	// reply: 8 = present, 9 = major opcode, 10 = first event, 11 = first error
	return p.Raw[9], p.Raw[10], p.Raw[8] == 1, nil
}

// queryVersion is required before any other XFixes request: the server refuses
// the rest until a client has declared which version it speaks.
func (x *x11Watch) queryVersion(major byte) error {
	b := make([]byte, 12)
	b[0] = major
	b[1] = xfixesQueryVer
	le.PutUint16(b[2:], uint16(len(b)/4))
	le.PutUint32(b[4:], xfixesMajor)
	le.PutUint32(b[8:], xfixesMinor)
	if _, err := x.conn.Write(b); err != nil {
		return err
	}
	p, err := x11.NextReply(x.r)
	if err != nil {
		return err
	}
	if p.IsError() {
		return fmt.Errorf("clipboard: XFixesQueryVersion failed with error %d", p.ErrorCode())
	}
	return nil
}

// selectSelectionInput asks for an event whenever the selection changes owner.
func (x *x11Watch) selectSelectionInput(major byte, window, selection uint32) error {
	b := make([]byte, 16)
	b[0] = major
	b[1] = xfixesSelectInput
	le.PutUint16(b[2:], uint16(len(b)/4))
	le.PutUint32(b[4:], window)
	le.PutUint32(b[8:], selection)
	le.PutUint32(b[12:], ownerNotifyMask)
	_, err := x.conn.Write(b)
	return err
}

// internAtom resolves an atom name, e.g. CLIPBOARD.
func (x *x11Watch) internAtom(name string) (uint32, error) {
	if _, err := x.conn.Write(x11.InternAtom(name, false)); err != nil {
		return 0, err
	}
	p, err := x11.NextReply(x.r)
	if err != nil {
		return 0, err
	}
	if p.IsError() {
		return 0, fmt.Errorf("clipboard: InternAtom(%s) failed with error %d", name, p.ErrorCode())
	}
	return p.Atom(), nil
}

func pad4(n int) int { return (n + 3) &^ 3 }

// startX11 sets up the watch and, once the server has accepted it, hands the
// connection to a goroutine that turns its events into ours. An error here
// means the caller should fall back to polling.
func startX11(ctx context.Context, w *Watcher, _ Options) error {
	x, setup, err := dialX11()
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			x.conn.Close()
		}
	}()
	if err := x.conn.SetDeadline(time.Now().Add(replyTimeout)); err != nil {
		return err
	}
	major, first, present, err := x.queryExtension("XFIXES")
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("clipboard: this X server has no XFIXES extension")
	}
	if err := x.queryVersion(major); err != nil {
		return err
	}
	clipboard, err := x.internAtom("CLIPBOARD")
	if err != nil {
		return err
	}
	if err := x.selectSelectionInput(major, setup.Root, clipboard); err != nil {
		return err
	}
	// The requests above are answered; from here the connection only waits.
	if err := x.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	x.first = first
	ok = true

	go func() {
		<-ctx.Done()
		x.conn.Close() // unblocks the read below
	}()
	go x.run(ctx, w)
	return nil
}

// run reports every selection-owner change until the context ends or the
// connection breaks. A broken connection ends the watcher rather than silently
// going deaf: Events closes, and the caller can start a new watcher.
func (x *x11Watch) run(ctx context.Context, w *Watcher) {
	defer close(w.events)
	defer x.conn.Close()
	var seq uint64
	for {
		p, err := x11.NextEvent(x.r)
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if p.EventCode() == x.first+selectionNotify {
			seq++
			w.send(seq)
		}
	}
}
