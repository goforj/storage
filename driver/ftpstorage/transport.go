package ftpstorage

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// ftpTransport owns the control socket and the current passive data socket of a session.
type ftpTransport struct {
	mu      sync.Mutex
	ctx     context.Context
	tls     *tls.Config
	control net.Conn
	data    net.Conn
	aborted bool
}

// dial uses the active request context for both FTP socket types.
func (t *ftpTransport) dial(network, address string) (net.Conn, error) {
	t.mu.Lock()
	ctx := t.ctx
	t.mu.Unlock()

	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.aborted {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	if t.control == nil {
		t.control = conn
	} else {
		t.data = conn
		// A custom dialer also owns FTPS data wrapping. The library wraps the control socket.
		if t.tls != nil {
			return tls.Client(conn, t.tls), nil
		}
	}
	return conn, nil
}

// abort closes the sockets so blocked reads, writes, and handshakes wake up.
func (t *ftpTransport) abort() {
	t.mu.Lock()
	t.aborted = true
	control, data := t.control, t.data
	t.mu.Unlock()
	if data != nil {
		_ = data.Close()
	}
	if control != nil {
		_ = control.Close()
	}
}

// run binds the serialized session to a request until its reply is consumed.
func (t *ftpTransport) run(ctx context.Context, fn func() error) error {
	t.mu.Lock()
	t.ctx = ctx
	t.mu.Unlock()
	stop := context.AfterFunc(ctx, t.abort)
	defer func() {
		stop()
		t.mu.Lock()
		t.ctx = nil
		t.mu.Unlock()
	}()
	err := fn()
	if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		return joinCleanup(ctx.Err(), err)
	}
	return err
}

// invokeFTP applies cancellation to real sockets while preserving the driver test seam.
func invokeFTP(ctx context.Context, conn ftpConn, fn func(ftpConn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if real, ok := conn.(realFTPConn); ok {
		return real.transport.run(ctx, func() error { return fn(conn) })
	}
	return fn(conn)
}
