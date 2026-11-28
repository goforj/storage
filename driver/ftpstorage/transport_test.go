package ftpstorage

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/storage/storagecore"
)

// TestFTPCanceledOverwrite exercises a hung final reply and recovery through real passive sockets.
func TestFTPCanceledOverwrite(t *testing.T) {
	t.Run("FTP", func(t *testing.T) { testFTPCanceledOverwrite(t, nil, false) })
	t.Run("Close during upload", func(t *testing.T) { testFTPCanceledOverwrite(t, nil, true) })
	t.Run("FTPS", func(t *testing.T) {
		certificates := httptest.NewUnstartedServer(nil)
		certificates.StartTLS()
		defer certificates.Close()
		testFTPCanceledOverwrite(t, certificates.TLS, false)
	})
}

// testFTPCanceledOverwrite checks that control and passive data sockets recover in both modes.
func testFTPCanceledOverwrite(t *testing.T, tlsConfig *tls.Config, shutdown bool) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	release := make(chan struct{})
	defer close(release)
	server := &ftpUploadFixture{
		files:   map[string]string{"file": "original"},
		staged:  make(chan struct{}),
		release: release,
		tls:     tlsConfig,
	}
	go server.serve(listener)

	cfg := Config{
		Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "test",
		TLS: tlsConfig != nil, InsecureSkipVerify: tlsConfig != nil,
	}
	disk, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	storage := disk.(storagecore.ContextStorage)
	t.Cleanup(func() { _ = disk.(io.Closer).Close() })
	testCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- storage.PutContext(ctx, "file", []byte("interrupted")) }()
	select {
	case <-server.staged:
	case <-testCtx.Done():
		t.Fatal("server never received the staged upload")
	}

	waitCtx, stopWaiting := context.WithTimeout(testCtx, 100*time.Millisecond)
	defer stopWaiting()
	if _, err := storage.GetContext(waitCtx, "file"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation waiting for FTP session = %v", err)
	}
	if shutdown {
		closed := make(chan error, 1)
		go func() { closed <- disk.(io.Closer).Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-testCtx.Done():
			t.Fatal("Close stayed blocked on the server")
		}
		if _, err := storage.GetContext(testCtx, "file"); !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("read after Close = %v", err)
		}
		newDisk, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = newDisk.(io.Closer).Close() })
		storage = newDisk.(storagecore.ContextStorage)
	} else {
		cancel()
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted Put = %v", err)
		}
	case <-testCtx.Done():
		t.Fatal("canceled Put stayed blocked on the server")
	}

	if body, err := storage.GetContext(testCtx, "file"); err != nil || string(body) != "original" {
		t.Fatalf("read after interruption = %q, %v", body, err)
	}
	server.mu.Lock()
	fileCount := len(server.files)
	server.mu.Unlock()
	if fileCount != 1 {
		t.Fatalf("server has %d files after cleanup, want 1", fileCount)
	}
	if err := storage.PutContext(testCtx, "file", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if body, err := storage.GetContext(testCtx, "file"); err != nil || string(body) != "replacement" {
		t.Fatalf("read after recovery = %q, %v", body, err)
	}
}

type ftpUploadFixture struct {
	mu      sync.Mutex
	files   map[string]string
	stall   sync.Once
	staged  chan struct{}
	release <-chan struct{}
	tls     *tls.Config
}

// serve accepts replacement connections while a prior transfer stops answering.
func (f *ftpUploadFixture) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go f.session(conn)
	}
}

// session implements the FTP replies needed to exercise staging and passive data transfers.
func (f *ftpUploadFixture) session(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	reply := func(message string) { _, _ = fmt.Fprint(conn, message+"\r\n") }
	reply("220 Test FTP")
	var passive net.Listener
	defer func() {
		if passive != nil {
			_ = passive.Close()
		}
	}()
	var renameFrom string
	commands := bufio.NewScanner(conn)
	for commands.Scan() {
		command, argument, _ := strings.Cut(commands.Text(), " ")
		switch command {
		case "AUTH":
			reply("234 Starting TLS")
			conn = tls.Server(conn, f.tls)
			commands = bufio.NewScanner(conn)
		case "PBSZ", "PROT":
			reply("200 Protected data connection")
		case "USER":
			reply("230 Logged in")
		case "FEAT":
			reply("502 No extensions")
		case "TYPE":
			reply("200 Binary mode")
		case "PASV":
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				reply("425 Passive listener failed")
				return
			}
			_ = listener.SetDeadline(time.Now().Add(5 * time.Second))
			if passive != nil {
				_ = passive.Close()
			}
			passive = listener
			port := listener.Addr().(*net.TCPAddr).Port
			reply(fmt.Sprintf("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256))
		case "STOR", "RETR":
			reply("150 Opening data connection")
			if !f.transfer(passive, command, argument) {
				reply("426 Transfer failed")
				return
			}
			if command == "STOR" {
				stalled := false
				f.stall.Do(func() { stalled = true })
				if stalled {
					close(f.staged)
					<-f.release
					return
				}
			}
			reply("226 Transfer complete")
		case "DELE":
			f.mu.Lock()
			delete(f.files, argument)
			f.mu.Unlock()
			reply("250 Deleted")
		case "RNFR":
			renameFrom = argument
			reply("350 Ready for destination")
		case "RNTO":
			f.mu.Lock()
			f.files[argument] = f.files[renameFrom]
			delete(f.files, renameFrom)
			f.mu.Unlock()
			reply("250 Renamed")
		case "QUIT":
			return
		default:
			reply("502 Command unsupported")
		}
	}
}

// transfer commits only the named staged file and leaves publication to the rename commands.
func (f *ftpUploadFixture) transfer(listener net.Listener, command, name string) bool {
	conn, err := listener.Accept()
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if f.tls != nil {
		conn = tls.Server(conn, f.tls)
		defer conn.Close()
	}
	if command == "STOR" {
		body, err := io.ReadAll(conn)
		if err != nil {
			return false
		}
		f.mu.Lock()
		f.files[name] = string(body)
		f.mu.Unlock()
		return true
	}
	f.mu.Lock()
	body, exists := f.files[name]
	f.mu.Unlock()
	_, err = io.WriteString(conn, body)
	return exists && err == nil
}
