package sftpstorage

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"time"

	"github.com/goforj/storage/storagecore"
)

// sftpSessions keeps one idle session and gives each operation exclusive transport ownership.
type sftpSessions struct {
	mu     sync.Mutex
	idle   sftpClient
	active map[sftpClient]struct{}
	dial   func(context.Context) (sftpClient, error)
	closed bool
}

// acquire reuses an idle session or opens a new one without holding the pool lock during I/O.
func (s *sftpSessions) acquire(ctx context.Context) (sftpClient, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fs.ErrClosed
	}
	client := s.idle
	s.idle = nil
	if client != nil {
		s.active[client] = struct{}{}
		s.mu.Unlock()
		return client, nil
	}
	s.mu.Unlock()

	client, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, joinCleanup(fs.ErrClosed, client.Close())
	}
	s.active[client] = struct{}{}
	s.mu.Unlock()
	return client, nil
}

// release retains a healthy connection only when the idle slot is empty.
func (s *sftpSessions) release(client sftpClient, reusable bool) {
	s.mu.Lock()
	delete(s.active, client)
	if reusable && !s.closed && s.idle == nil {
		s.idle = client
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_ = client.Close()
}

// close stops idle and active sessions and prevents new leases.
func (s *sftpSessions) close() error {
	s.mu.Lock()
	s.closed = true
	idle, active := s.idle, s.active
	s.idle, s.active = nil, nil
	s.mu.Unlock()

	var err error
	if idle != nil {
		err = idle.Close()
	}
	for client := range active {
		err = errors.Join(err, client.Close())
	}
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
	defer func() {
		stopped := stop()
		if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = joinCleanup(ctx.Err(), err)
		}
		d.sessions.release(client, stopped && ctx.Err() == nil && (err == nil || errors.Is(err, storagecore.ErrNotFound) || errors.Is(err, storagecore.ErrForbidden)))
	}()
	return run(&driver{client: client, prefix: d.prefix, reconnect: d.sessions.dial})
}

// runSFTPCommand leases a transport for operations that only return an error.
func runSFTPCommand(ctx context.Context, d *driver, run func(*driver) error) error {
	_, err := runSFTP(ctx, d, func(session *driver) (struct{}, error) {
		return struct{}{}, run(session)
	})
	return err
}

// removeWithSFTPReconnect attempts to clean a staged object after a transport failure.
func removeWithSFTPReconnect(ctx context.Context, reconnect func(context.Context) (sftpClient, error), path string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	client, err := reconnect(ctx)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	removeErr := client.Remove(path)
	stop()
	return joinCleanup(removeErr, client.Close())
}
