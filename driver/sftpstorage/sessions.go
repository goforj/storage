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
	mu        sync.Mutex
	idle      sftpClient
	active    map[sftpClient]struct{}
	dial      func(context.Context) (sftpClient, error)
	lifetime  context.Context
	stop      context.CancelFunc
	dialSlots chan struct{}
	dials     sync.WaitGroup
}

// newSFTPSessions limits simultaneous handshakes while allowing established operations to overlap.
func newSFTPSessions(idle sftpClient, dial func(context.Context) (sftpClient, error)) *sftpSessions {
	lifetime, stop := context.WithCancel(context.Background())
	return &sftpSessions{
		idle: idle, active: make(map[sftpClient]struct{}), dial: dial,
		lifetime: lifetime, stop: stop, dialSlots: make(chan struct{}, 2),
	}
}

// acquire reuses an idle session or queues for a bounded handshake.
func (s *sftpSessions) acquire(ctx context.Context) (sftpClient, error) {
	s.mu.Lock()
	if s.lifetime.Err() != nil {
		s.mu.Unlock()
		return nil, fs.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if client := s.takeIdleLocked(); client != nil {
		s.mu.Unlock()
		return client, nil
	}
	s.dials.Add(1)
	s.mu.Unlock()
	defer s.dials.Done()

	select {
	case s.dialSlots <- struct{}{}:
		defer func() { <-s.dialSlots }()
	case <-s.lifetime.Done():
		return nil, fs.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	s.mu.Lock()
	if s.lifetime.Err() != nil {
		s.mu.Unlock()
		return nil, fs.ErrClosed
	}
	if client := s.takeIdleLocked(); client != nil {
		s.mu.Unlock()
		return client, nil
	}
	s.mu.Unlock()

	setup, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.lifetime, cancel)
	defer stop()
	client, err := s.dial(setup)
	if err != nil {
		if s.lifetime.Err() != nil {
			return nil, joinCleanup(fs.ErrClosed, err)
		}
		return nil, err
	}
	s.mu.Lock()
	if s.lifetime.Err() != nil {
		s.mu.Unlock()
		return nil, joinCleanup(fs.ErrClosed, client.Close())
	}
	s.active[client] = struct{}{}
	s.mu.Unlock()
	return client, nil
}

// takeIdleLocked reserves the idle transport for the caller before it drops the pool lock.
func (s *sftpSessions) takeIdleLocked() sftpClient {
	client := s.idle
	if client != nil {
		s.idle = nil
		s.active[client] = struct{}{}
	}
	return client
}

// release retains a healthy connection only when the idle slot is empty.
func (s *sftpSessions) release(client sftpClient, reusable bool) {
	s.mu.Lock()
	if reusable && s.lifetime.Err() == nil && s.idle == nil {
		delete(s.active, client)
		s.idle = client
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_ = client.Close()
	s.mu.Lock()
	delete(s.active, client)
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
	if idle != nil {
		err = idle.Close()
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
	defer func() {
		stopped := stop()
		if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = joinCleanup(ctx.Err(), err)
		}
		d.sessions.release(client, stopped && ctx.Err() == nil && (err == nil || errors.Is(err, storagecore.ErrNotFound) || errors.Is(err, storagecore.ErrForbidden)))
	}()
	return run(&driver{client: client, prefix: d.prefix, cleanupTemporary: d.sessions.removeTemporary})
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
