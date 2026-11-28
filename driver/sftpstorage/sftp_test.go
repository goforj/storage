package sftpstorage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/goforj/storage/storagecore"
)

// TestSFTPWithEmbeddedServer verifies a real SSH/SFTP session can upload and download data.
func TestSFTPWithEmbeddedServer(t *testing.T) {
	root := t.TempDir()

	hostSigner, err := generateSigner()
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "test" && string(pass) == "test" {
				return nil, nil
			}
			return nil, fmt.Errorf("permission denied")
		},
	}
	serverConfig.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go acceptLoop(ln, serverConfig, func(channel ssh.Channel) {
		defer channel.Close()
		server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
		if err == nil {
			defer server.Close()
			_ = server.Serve()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fs, err := newFromDiskConfig(ctx, (Config{
		Host:                  "127.0.0.1",
		Port:                  ln.Addr().(*net.TCPAddr).Port,
		User:                  "test",
		Password:              "test",
		InsecureIgnoreHostKey: true,
	}).ResolvedConfig())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = fs.(io.Closer).Close() })
	cancel()

	if err := fs.Put("hello.txt", []byte("sftp")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := fs.Get("hello.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "sftp" {
		t.Fatalf("Get = %q", got)
	}

	// The embedded server leaves the channel open when it finishes serving.
	if err := fs.(io.Closer).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSFTPSetupHandshakeDeadline verifies that a silent peer cannot block setup past its deadline.
func TestSFTPSetupHandshakeDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
	}()

	cfg := Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Password: "test", InsecureIgnoreHostKey: true}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := newFromDiskConfig(ctx, cfg.ResolvedConfig()); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("setup error = %v, want handshake deadline", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("timed-out setup left the connection open")
	}
}

// TestSFTPSetupSubsystemCancellation verifies setup cancellation after SSH authentication.
func TestSFTPSetupSubsystemCancellation(t *testing.T) {
	signer, err := generateSigner()
	if err != nil {
		t.Fatal(err)
	}
	serverCfg := &ssh.ServerConfig{NoClientAuth: true}
	serverCfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	requested := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		server, channels, requests, err := ssh.NewServerConn(conn, serverCfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		pending, ok := <-channels
		if !ok {
			return
		}
		channel, sessionRequests, err := pending.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		if req, ok := <-sessionRequests; ok && req.Type == "subsystem" {
			close(requested)
			_ = server.Wait()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg := Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Password: "test", InsecureIgnoreHostKey: true}
	result := make(chan error, 1)
	go func() {
		_, err := newFromDiskConfig(ctx, cfg.ResolvedConfig())
		result <- err
	}()
	select {
	case <-requested:
		cancel()
	case <-ctx.Done():
		t.Fatal("server never received the SFTP subsystem request")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "sftp client") {
			t.Fatalf("setup error = %v, want canceled subsystem", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SFTP subsystem setup ignored cancellation")
	}
}

// TestSFTPSetupDialError verifies that transport failures keep their setup phase.
func TestSFTPSetupDialError(t *testing.T) {
	_, err := openSFTPClient(context.Background(), "127.0.0.1:bad-port", &ssh.ClientConfig{Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "sftp dial") {
		t.Fatalf("setup error = %v, want dial failure", err)
	}
}

// TestSFTPClosePreservesTransportErrors verifies explicit close failures remain observable.
func TestSFTPClosePreservesTransportErrors(t *testing.T) {
	closeErr := errors.New("write channel close failed")
	// The frame contains an SFTP VERSION response with protocol version 3.
	version := []byte{0, 0, 0, 5, 2, 0, 0, 0, 3}
	client, err := sftp.NewClientPipe(bytes.NewReader(version), &fakeWriteCloser{closeErr: closeErr})
	if err != nil {
		t.Fatalf("new SFTP client: %v", err)
	}
	c := &realSFTPClient{client: client}
	if err := c.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v, want %v", err, closeErr)
	}
}

// TestSFTPCanceledOverwrite verifies session isolation, staged-file cleanup, and recovery on real sockets.
func TestSFTPCanceledOverwrite(t *testing.T) {
	t.Run("open reply", func(t *testing.T) { testSFTPCanceledOverwrite(t, true) })
	t.Run("write reply", func(t *testing.T) { testSFTPCanceledOverwrite(t, false) })
}

// testSFTPCanceledOverwrite interrupts creation or writing after the server has created the stage.
func testSFTPCanceledOverwrite(t *testing.T, stallOpen bool) {
	t.Helper()
	hostSigner, err := generateSigner()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	handlers := sftp.InMemHandler()
	uploads := &stallingSFTPWriter{
		FileWriter: handlers.FilePut, stallOpen: stallOpen, started: make(chan struct{}), release: make(chan struct{}),
	}
	handlers.FilePut = uploads
	defer close(uploads.release)
	go acceptLoop(listener, cfg, func(channel ssh.Channel) {
		requests := sftp.NewRequestServer(channel, handlers)
		defer requests.Close()
		_ = requests.Serve()
	})

	testCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	disk, err := newFromDiskConfig(testCtx, (Config{
		Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "test", Password: "test", InsecureIgnoreHostKey: true,
	}).ResolvedConfig())
	if err != nil {
		t.Fatal(err)
	}
	storage := disk.(storagecore.ContextStorage)
	t.Cleanup(func() { _ = disk.(io.Closer).Close() })

	if err := storage.PutContext(testCtx, "file", []byte("original")); err != nil {
		t.Fatal(err)
	}
	uploads.stall.Store(true)
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	failed := make(chan error, 1)
	go func() { failed <- storage.PutContext(ctx, "file", []byte("interrupted")) }()

	select {
	case <-uploads.started:
	case <-testCtx.Done():
		t.Fatal("server did not reach the staged write")
	}
	if body, err := storage.GetContext(testCtx, "file"); err != nil || string(body) != "original" {
		t.Fatalf("concurrent read = %q, %v", body, err)
	}
	cancel()
	select {
	case err := <-failed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted Put = %v", err)
		}
	case <-testCtx.Done():
		t.Fatal("canceled Put stayed blocked on the server")
	}
	entries, err := storage.ListContext(testCtx, "")
	if err != nil || len(entries) != 1 || entries[0].Path != "file" {
		t.Fatalf("listing after interruption = %+v, %v", entries, err)
	}
	if err := storage.PutContext(testCtx, "file", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if body, err := storage.GetContext(testCtx, "file"); err != nil || string(body) != "replacement" {
		t.Fatalf("read after recovery = %q, %v", body, err)
	}
}

type stallingSFTPWriter struct {
	sftp.FileWriter
	stall     atomic.Bool
	stallOpen bool
	started   chan struct{}
	release   chan struct{}
}

// Filewrite pauses the selected staging write after the server has created the temporary file.
func (f *stallingSFTPWriter) Filewrite(request *sftp.Request) (io.WriterAt, error) {
	writer, err := f.FileWriter.Filewrite(request)
	if err == nil && f.stall.Swap(false) {
		if f.stallOpen {
			close(f.started)
			<-f.release
			return writer, nil
		}
		return stalledSFTPFile{WriterAt: writer, started: f.started, release: f.release}, nil
	}
	return writer, err
}

type stalledSFTPFile struct {
	io.WriterAt
	started chan struct{}
	release <-chan struct{}
}

// WriteAt waits for test teardown so only client-side cancellation can unblock the operation.
func (f stalledSFTPFile) WriteAt(data []byte, offset int64) (int, error) {
	close(f.started)
	<-f.release
	return f.WriterAt.WriteAt(data, offset)
}

// acceptLoop upgrades accepted loopback connections into embedded SSH sessions.
func acceptLoop(ln net.Listener, cfg *ssh.ServerConfig, serve func(ssh.Channel)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			_, chans, reqs, err := ssh.NewServerConn(c, cfg)
			if err != nil {
				_ = c.Close()
				return
			}
			go ssh.DiscardRequests(reqs)
			handleChannels(chans, serve)
		}(conn)
	}
}

// handleChannels accepts only session channels that request the SFTP subsystem.
func handleChannels(chans <-chan ssh.NewChannel, serve func(ssh.Channel)) {
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}

		go func(in <-chan *ssh.Request) {
			for req := range in {
				switch req.Type {
				case "subsystem":
					var payload struct {
						Name string
					}
					if err := ssh.Unmarshal(req.Payload, &payload); err == nil && payload.Name == "sftp" {
						req.Reply(true, nil)
						serve(channel)
						return
					}
					req.Reply(false, nil)
				default:
					req.Reply(false, nil)
				}
			}
		}(requests)
	}
}

// generateSigner creates an ephemeral RSA host key for the embedded SSH server.
func generateSigner() (ssh.Signer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(key)
}
