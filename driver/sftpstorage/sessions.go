package sftpstorage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"sync"
	"time"

	"github.com/goforj/storage/storagecore"
	"github.com/pkg/sftp"
)

const (
	maxSFTPSessions   = 8
	maxSFTPHandshakes = 2
)

// sftpSessions bounds the connections per disk and lends each transport to one operation at a time.
type sftpSessions struct {
	mu       sync.Mutex
	idle     []sftpClient
	active   map[sftpClient]struct{}
	opening  int
	changed  chan struct{}
	dial     func(context.Context) (sftpClient, error)
	lifetime context.Context
	stop     context.CancelFunc
	dials    sync.WaitGroup
}

// newSFTPSessions owns the initial transport and limits subsequent connection setup.
func newSFTPSessions(idle sftpClient, dial func(context.Context) (sftpClient, error)) *sftpSessions {
	lifetime, stop := context.WithCancel(context.Background())
	s := &sftpSessions{
		active: make(map[sftpClient]struct{}), dial: dial,
		lifetime: lifetime, stop: stop,
	}
	if idle != nil {
		s.idle = append(s.idle, idle)
	}
	return s
}

// acquire prefers a reusable session, then reserves capacity for a handshake if available.
func (s *sftpSessions) acquire(ctx context.Context) (sftpClient, error) {
	for {
		s.mu.Lock()
		if s.lifetime.Err() != nil {
			s.mu.Unlock()
			return nil, fs.ErrClosed
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if n := len(s.idle); n != 0 {
			client := s.idle[n-1]
			s.idle[n-1] = nil
			s.idle = s.idle[:n-1]
			s.active[client] = struct{}{}
			s.mu.Unlock()
			return client, nil
		}
		if s.opening < maxSFTPHandshakes && len(s.active)+s.opening < maxSFTPSessions {
			s.opening++
			s.dials.Add(1)
			s.mu.Unlock()
			return s.open(ctx)
		}
		if s.changed == nil {
			s.changed = make(chan struct{})
		}
		changed := s.changed
		s.mu.Unlock()

		select {
		case <-changed:
		case <-s.lifetime.Done():
			return nil, fs.ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// open registers a new session or discards it when shutdown wins the race.
func (s *sftpSessions) open(ctx context.Context) (sftpClient, error) {
	defer s.dials.Done()
	setup, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.lifetime, cancel)
	defer stop()
	client, err := s.dial(setup)

	s.mu.Lock()
	s.opening--
	s.signalLocked()
	closed := s.lifetime.Err() != nil
	if err == nil && !closed {
		s.active[client] = struct{}{}
	}
	s.mu.Unlock()
	if closed {
		if err == nil {
			err = client.Close()
		}
		return nil, joinCleanup(fs.ErrClosed, err)
	}
	return client, err
}

// signalLocked wakes all waiters so they can choose between reuse and available dial capacity.
func (s *sftpSessions) signalLocked() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// release returns healthy transports to the pool and counts unhealthy ones until they close.
func (s *sftpSessions) release(client sftpClient, reusable bool) {
	if !reusable {
		_ = client.Close()
	}
	s.mu.Lock()
	delete(s.active, client)
	if reusable && s.lifetime.Err() == nil {
		s.idle = append(s.idle, client)
	}
	s.signalLocked()
	s.mu.Unlock()
}

// close stops idle and active sessions and prevents new leases.
func (s *sftpSessions) close() error {
	s.mu.Lock()
	s.stop()
	idle, active := s.idle, s.active
	s.idle, s.active = nil, nil
	s.mu.Unlock()

	var err error
	for _, client := range idle {
		err = errors.Join(err, client.Close())
	}
	for client := range active {
		err = errors.Join(err, client.Close())
	}
	s.dials.Wait()
	return err
}

// runSFTP binds cancellation to the operation's transport, leaving other callers connected.
func runSFTP[T any](ctx context.Context, d *driver, run func(*driver) (T, error)) (value T, err error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return value, err
	}
	client, err := d.sessions.acquire(ctx)
	if err != nil {
		return value, err
	}
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	var temporary string
	defer func() {
		stopped := stop()
		if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = joinCleanup(ctx.Err(), err)
		}
		reusable := err == nil || (!sftpConnectionLost(err) &&
			(errors.Is(err, storagecore.ErrNotFound) || errors.Is(err, storagecore.ErrForbidden)))
		d.sessions.release(client, reusable && stopped && ctx.Err() == nil && temporary == "")
		if temporary != "" {
			cleanupErr := wrapError(d.sessions.removeTemporary(ctx, temporary))
			if !errors.Is(cleanupErr, storagecore.ErrNotFound) {
				err = joinCleanup(err, cleanupErr)
			}
		}
	}()
	return run(&driver{client: client, prefix: d.prefix, cleanupTemporary: func(_ context.Context, path string) error {
		// Cleanup must borrow only after the failed operation releases its slot.
		temporary = path
		return nil
	}})
}

// runSFTPRead retries once after transport loss, before returning any read results to the caller.
func runSFTPRead[T any](ctx context.Context, d *driver, run func(*driver) (T, error)) (T, error) {
	ctx = normalizeContext(ctx)
	result, err := runSFTP(ctx, d, run)
	if err == nil || ctx.Err() != nil || !sftpConnectionLost(err) {
		return result, err
	}
	return runSFTP(ctx, d, run)
}

// sftpConnectionLost recognizes both SFTP receive failures and SSH writes racing with a disconnect.
func sftpConnectionLost(err error) bool {
	return errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}

// runSFTPCommand leases a transport for operations that only return an error.
func runSFTPCommand(ctx context.Context, d *driver, run func(*driver) error) error {
	_, err := runSFTP(ctx, d, func(session *driver) (struct{}, error) {
		return struct{}{}, run(session)
	})
	return err
}

// removeTemporary gives failed writes a short cleanup window while the disk is open.
func (s *sftpSessions) removeTemporary(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	client, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	removeErr := client.Remove(path)
	stopped := stop()
	s.release(client, stopped && ctx.Err() == nil && removeErr == nil)
	return removeErr
}
