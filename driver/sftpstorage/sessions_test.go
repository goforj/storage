package sftpstorage

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSFTPShutdownCancelsQueuedAndActiveHandshakes verifies the handshake bound and shutdown ownership.
func TestSFTPShutdownCancelsQueuedAndActiveHandshakes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 3)
	var unfinished atomic.Int32
	sessions := newSFTPSessions(nil, func(ctx context.Context) (sftpClient, error) {
		unfinished.Add(1)
		defer unfinished.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	t.Cleanup(func() { _ = sessions.close() })

	results := make(chan error, 3)
	for range 2 {
		go func() { _, err := sessions.acquire(ctx); results <- err }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("handshake did not start")
		}
	}

	waiting, stopWaiting := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stopWaiting()
	if _, err := sessions.acquire(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued acquisition = %v", err)
	}
	select {
	case <-entered:
		t.Fatal("more than two handshakes started")
	default:
	}

	go func() { _, err := sessions.acquire(ctx); results <- err }()
	if err := sessions.close(); err != nil {
		t.Fatal(err)
	}
	if active := unfinished.Load(); active != 0 {
		t.Fatalf("Close left %d handshakes active", active)
	}
	for range 3 {
		select {
		case err := <-results:
			if !errors.Is(err, fs.ErrClosed) {
				t.Fatalf("acquire after shutdown = %v", err)
			}
		case <-ctx.Done():
			t.Fatal("acquisition outlived Close")
		}
	}
	if err := sessions.removeTemporary(ctx, ".storage-tmp-test"); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("cleanup after shutdown = %v", err)
	}
	select {
	case <-entered:
		t.Fatal("a new handshake started during shutdown")
	default:
	}
}

// TestSFTPTemporaryCleanupReportsReconnectFailure preserves both the failed write and failed recovery.
func TestSFTPTemporaryCleanupReportsReconnectFailure(t *testing.T) {
	writeErr := errors.New("write failed")
	removeErr := errors.New("connection lost")
	dialErr := errors.New("reconnect refused")
	sessions := newSFTPSessions(nil, func(context.Context) (sftpClient, error) {
		return nil, dialErr
	})
	t.Cleanup(func() { _ = sessions.close() })
	disk := &driver{client: &fakeSFTPClient{removeErr: removeErr}, cleanupTemporary: sessions.removeTemporary}
	err := disk.discardTemporary(context.Background(), ".storage-tmp-test", nil, writeErr)
	if !errors.Is(err, writeErr) || !errors.Is(err, dialErr) {
		t.Fatalf("cleanup result = %v, want write and reconnect errors", err)
	}
}

// TestSFTPShutdownClosesLateHandshake discards a connection that completes during shutdown.
func TestSFTPShutdownClosesLateHandshake(t *testing.T) {
	started := make(chan struct{})
	closeErr := errors.New("late client closed")
	sessions := newSFTPSessions(nil, func(ctx context.Context) (sftpClient, error) {
		close(started)
		<-ctx.Done()
		return &fakeSFTPClient{closeErr: closeErr}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan error, 1)
	go func() { _, err := sessions.acquire(ctx); got <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("dial did not start")
	}
	if err := sessions.close(); err != nil {
		t.Fatal(err)
	}
	if err := <-got; !errors.Is(err, fs.ErrClosed) || !errors.Is(err, closeErr) {
		t.Fatalf("late handshake = %v", err)
	}
}

// TestSFTPLeaseCancellationPreservesOperationError preserves context and transport errors together.
func TestSFTPLeaseCancellationPreservesOperationError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := newSFTPSessions(&fakeSFTPClient{}, func(context.Context) (sftpClient, error) {
		t.Fatal("unexpected handshake")
		return nil, nil
	})
	t.Cleanup(func() { _ = sessions.close() })
	wireErr := errors.New("transport disconnected")
	err := runSFTPCommand(ctx, &driver{sessions: sessions}, func(*driver) error {
		cancel()
		return wireErr
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, wireErr) {
		t.Fatalf("operation error = %v, want cancellation and transport errors", err)
	}
	if _, err := sessions.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire = %v", err)
	}
	if err := runSFTPCommand(ctx, &driver{sessions: sessions}, func(*driver) error {
		t.Fatal("pre-canceled scope ran an operation")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled operation = %v", err)
	}
}

// TestSFTPShutdownReleasesHandshakeWaiters verifies shutdown wakes borrowers even when every slot stays occupied.
func TestSFTPShutdownReleasesHandshakeWaiters(t *testing.T) {
	sessions := newSFTPSessions(nil, func(context.Context) (sftpClient, error) {
		t.Error("shutdown attempted a handshake")
		return nil, fs.ErrClosed
	})
	for range cap(sessions.dialSlots) {
		sessions.dialSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitCtx := &sftpWaitContext{Context: ctx, waiting: make(chan struct{})}
	got := make(chan error, 1)
	go func() { _, err := sessions.acquire(waitCtx); got <- err }()
	select {
	case <-waitCtx.waiting:
	case <-ctx.Done():
		t.Fatal("borrower did not reach its queue")
	}
	if err := sessions.close(); err != nil {
		t.Fatal(err)
	}
	if err := <-got; !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("queued acquisition = %v", err)
	}
}

type sftpWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

// Done signals the test when a borrower evaluates its cancellation channel.
func (c *sftpWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}
