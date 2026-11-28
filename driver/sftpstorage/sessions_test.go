package sftpstorage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
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
	sessions.opening = maxSFTPHandshakes
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

// TestSFTPQueuedBorrowerUsesReturnedSession ensures a healthy release wakes a borrower during stalled setup.
func TestSFTPQueuedBorrowerUsesReturnedSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	healthy := &fakeSFTPClient{}
	entered := make(chan struct{}, maxSFTPHandshakes)
	sessions := newSFTPSessions(healthy, func(ctx context.Context) (sftpClient, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	t.Cleanup(func() { _ = sessions.close() })

	held, err := sessions.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range maxSFTPHandshakes {
		go func() { _, _ = sessions.acquire(ctx) }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("handshake did not start")
		}
	}

	waitCtx := &sftpWaitContext{Context: ctx, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		client, err := sessions.acquire(waitCtx)
		if err == nil {
			sessions.release(client, true)
			if client != healthy {
				err = errors.New("borrower obtained a different session")
			}
		}
		result <- err
	}()
	select {
	case <-waitCtx.waiting:
	case <-ctx.Done():
		t.Fatal("borrower did not queue")
	}
	sessions.release(held, true)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("healthy session did not wake the borrower")
	}
}

// TestSFTPConnectionLimitAndReuse holds a burst of connections and verifies that queued callers reuse them.
func TestSFTPConnectionLimitAndReuse(t *testing.T) {
	const callers = 32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var opened atomic.Int32
	sessions := newSFTPSessions(&fakeSFTPClient{}, func(context.Context) (sftpClient, error) {
		opened.Add(1)
		return &fakeSFTPClient{}, nil
	})
	t.Cleanup(func() { _ = sessions.close() })
	acquired := make(chan struct{}, callers)
	done := make(chan error, callers)
	unblock := make(chan struct{})
	defer close(unblock)
	for range callers {
		go func() {
			client, err := sessions.acquire(ctx)
			if err == nil {
				acquired <- struct{}{}
				select {
				case <-unblock:
				case <-ctx.Done():
				}
				sessions.release(client, true)
			}
			done <- err
		}()
	}

	for range maxSFTPSessions {
		select {
		case <-acquired:
		case <-ctx.Done():
			t.Fatal("pool did not reach capacity")
		}
	}
	waiting, stopWaiting := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stopWaiting()
	if _, err := sessions.acquire(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire at capacity = %v", err)
	}
	for range callers {
		// Each token releases one borrower, making room for another without reconnecting.
		select {
		case unblock <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("queued caller could not use a returned session")
		}
	}
	for range callers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if actual := opened.Load(); actual != maxSFTPSessions-1 {
		t.Fatalf("opened %d additional connections, want %d", actual, maxSFTPSessions-1)
	}
}

// TestSFTPFailedWriteReleasesCapacityForCleanup checks cleanup when all other sessions are busy.
func TestSFTPFailedWriteReleasesCapacityForCleanup(t *testing.T) {
	t.Run("remove stage", func(t *testing.T) { testSFTPFailedWriteCleanup(t, nil) })
	t.Run("stage already absent", func(t *testing.T) { testSFTPFailedWriteCleanup(t, fs.ErrNotExist) })
}

// testSFTPFailedWriteCleanup checks recovery over a replacement session at full capacity.
func testSFTPFailedWriteCleanup(t *testing.T, cleanupErr error) {
	t.Helper()
	lost := sftp.ErrSSHFxConnectionLost
	failed := &fakeSFTPClient{openFileErr: lost, removeErr: lost}
	cleanup := &fakeSFTPClient{removeErr: cleanupErr}
	var opened int
	sessions := newSFTPSessions(failed, func(context.Context) (sftpClient, error) {
		opened++
		if opened == maxSFTPSessions {
			return cleanup, nil
		}
		return &fakeSFTPClient{}, nil
	})
	t.Cleanup(func() { _ = sessions.close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := sessions.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range maxSFTPSessions - 1 {
		if _, err := sessions.acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sessions.release(client, true)

	if err := (&driver{sessions: sessions}).PutContext(ctx, "file", []byte("body")); !errors.Is(err, lost) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed write = %v", err)
	}
	if len(cleanup.removePaths) != 1 || cleanup.removePaths[0] != failed.openFilePath {
		t.Fatalf("cleanup removed %v, expected %q", cleanup.removePaths, failed.openFilePath)
	}
}

// TestSFTPReadRecoveryIsBounded verifies retryable errors, non-retryable errors, and cancellation.
func TestSFTPReadRecoveryIsBounded(t *testing.T) {
	for _, test := range []struct {
		name        string
		first       error
		cancel      bool
		wantRetries int
		want        error
	}{
		{"connection lost", sftp.ErrSSHFxConnectionLost, false, 1, sftp.ErrSSHFxConnectionLost},
		{"SSH channel closed", io.EOF, false, 1, sftp.ErrSSHFxConnectionLost},
		{"partial SSH packet", io.ErrUnexpectedEOF, false, 1, sftp.ErrSSHFxConnectionLost},
		{"closed socket", net.ErrClosed, false, 1, sftp.ErrSSHFxConnectionLost},
		{"operation error", fs.ErrPermission, false, 0, fs.ErrPermission},
		{"caller canceled", sftp.ErrSSHFxConnectionLost, true, 0, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := &fakeSFTPClient{openErr: test.first}
			if test.cancel {
				first.openHook = cancel
			}
			var dials int
			sessions := newSFTPSessions(first, func(context.Context) (sftpClient, error) {
				dials++
				return &fakeSFTPClient{openErr: sftp.ErrSSHFxConnectionLost}, nil
			})
			d := &driver{sessions: sessions}
			defer d.Close()

			_, err := d.GetContext(ctx, "file")
			if !errors.Is(err, test.want) || dials != test.wantRetries {
				t.Fatalf("Get = %v after %d replacement dials, want %v and %d", err, dials, test.want, test.wantRetries)
			}
		})
	}
}

// TestSFTPPutDoesNotRetryTransportLoss verifies writes never use the read retry path.
func TestSFTPPutDoesNotRetryTransportLoss(t *testing.T) {
	lost := sftp.ErrSSHFxConnectionLost
	sessions := newSFTPSessions(&fakeSFTPClient{mkdirErr: lost}, func(context.Context) (sftpClient, error) {
		t.Error("Put attempted to open a replacement transport")
		return &fakeSFTPClient{}, nil
	})
	d := &driver{sessions: sessions}
	defer d.Close()

	if err := d.Put("file", []byte("payload")); !errors.Is(err, lost) {
		t.Fatalf("Put = %v, want connection lost", err)
	}
}

// TestSFTPFreshBorrowRespectsPoolCapacity replaces an idle transport before a fresh handshake.
func TestSFTPFreshBorrowRespectsPoolCapacity(t *testing.T) {
	live := 0
	newClient := func() sftpClient {
		live++
		return &sftpCountedClient{onClose: func() { live-- }}
	}
	sessions := newSFTPSessions(newClient(), func(context.Context) (sftpClient, error) {
		if live >= maxSFTPSessions {
			t.Errorf("handshake started with %d live clients", live)
		}
		return newClient(), nil
	})
	t.Cleanup(func() { _ = sessions.close() })

	clients := make([]sftpClient, 0, maxSFTPSessions)
	for range cap(clients) {
		client, err := sessions.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
	}
	for _, client := range clients {
		sessions.release(client, true)
	}
	fresh, err := sessions.borrow(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range clients {
		if fresh == original {
			t.Fatal("fresh borrow reused an idle client")
		}
	}
	if live != maxSFTPSessions {
		t.Fatalf("live clients after replacement = %d", live)
	}
	sessions.release(fresh, true)
}

type sftpCountedClient struct {
	fakeSFTPClient
	onClose func()
}

// Close records when the session slot is physically freed.
func (c *sftpCountedClient) Close() error {
	c.onClose()
	return nil
}

// TestSFTPReadReportsSessionSetupFailure verifies that a failed lease surfaces its setup error.
func TestSFTPReadReportsSessionSetupFailure(t *testing.T) {
	setupErr := errors.New("SSH authentication refused")
	dials := 0
	sessions := newSFTPSessions(nil, func(context.Context) (sftpClient, error) {
		dials++
		return nil, setupErr
	})
	disk := &driver{sessions: sessions}
	t.Cleanup(func() { _ = disk.Close() })

	got, err := disk.Get("file")
	if got != nil || !errors.Is(err, setupErr) || dials != 1 {
		t.Fatalf("Get after failed setup = %q, %v after %d dial attempts", got, err, dials)
	}
}

// TestSFTPTemporaryCleanupDeadlineClosesTransport verifies a silent peer cannot strand cleanup.
func TestSFTPTemporaryCleanupDeadlineClosesTransport(t *testing.T) {
	client := &sftpStalledRemoval{closed: make(chan struct{})}
	sessions := newSFTPSessions(client, func(context.Context) (sftpClient, error) {
		t.Error("cleanup dialed instead of using the available session")
		return nil, net.ErrClosed
	})
	t.Cleanup(func() { _ = sessions.close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- sessions.removeTemporary(ctx, ".storage-tmp-test") }()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("cleanup = %v, want connection closed by its deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup remained blocked after its deadline")
	}
}

type sftpStalledRemoval struct {
	fakeSFTPClient
	closed chan struct{}
	once   sync.Once
}

// Remove simulates a server that leaves the cleanup request unanswered.
func (c *sftpStalledRemoval) Remove(string) error {
	<-c.closed
	return net.ErrClosed
}

// Close wakes the blocked removal when the cleanup deadline expires.
func (c *sftpStalledRemoval) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
