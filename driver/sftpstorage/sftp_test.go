package sftpstorage

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/goforj/storage"
	storagetest "github.com/goforj/storage/storagetest"
)

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

	go acceptLoop(t, ln, serverConfig, root)

	cfg := storage.Config{
		Default: "sftp",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"sftp": Config{
				Host:                  "127.0.0.1",
				Port:                  ln.Addr().(*net.TCPAddr).Port,
				User:                  "test",
				Password:              "test",
				InsecureIgnoreHostKey: true,
			},
		},
	}

	// brief delay to ensure server is accepting
	time.Sleep(100 * time.Millisecond)

	mgr, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	fs, err := mgr.Disk("sftp")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	storagetest.RunStorageContractTests(t, fs)
}

func acceptLoop(t *testing.T, ln net.Listener, cfg *ssh.ServerConfig, root string) {
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
			go handleChannels(t, chans, root)
		}(conn)
	}
}

func handleChannels(t *testing.T, chans <-chan ssh.NewChannel, root string) {
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
						server, err := sftp.NewServer(channel, sftp.WithDebug(nil), sftp.WithServerWorkingDirectory(root))
						if err != nil {
							req.Reply(false, nil)
							return
						}
						if err := server.Serve(); err == io.EOF {
							_ = server.Close()
						}
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

func generateSigner() (ssh.Signer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(key)
}
