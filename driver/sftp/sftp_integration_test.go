//go:build integration

package sftpdriver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestSFTPIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)

	host := getenv("INTEGRATION_SFTP_HOST", "127.0.0.1")
	port := getenvInt("INTEGRATION_SFTP_PORT", 2222)
	user := getenv("INTEGRATION_SFTP_USER", "fsuser")
	pass := getenv("INTEGRATION_SFTP_PASS", "fspass")

	addr := fmt.Sprintf("%s:%d", host, port)
	if !filesystemtest.Reachable(addr) {
		t.Skipf("sftp endpoint not reachable at %s; ensure docker-compose is up", addr)
	}

	cfg := filesystem.Config{
		Default: "sftp",
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"sftp": {
				Driver:                    "sftp",
				SFTPHost:                  host,
				SFTPPort:                  port,
				SFTPUser:                  user,
				SFTPPassword:              pass,
				SFTPInsecureIgnoreHostKey: true,
				Prefix:                    "integration",
			},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("SFTP integration manager init failed: %v", err)
	}
	fs, err := mgr.Disk("sftp")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}
	filesystemtest.RunFilesystemContractTests(t, fs)
}

func TestSFTPKeyAuthIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)

	host := getenv("INTEGRATION_SFTP_HOST", "127.0.0.1")
	port := getenvInt("INTEGRATION_SFTP_PORT", 2222)
	user := getenv("INTEGRATION_SFTP_USER", "fsuser")
	pass := getenv("INTEGRATION_SFTP_PASS", "fspass")

	addr := fmt.Sprintf("%s:%d", host, port)
	if !filesystemtest.Reachable(addr) {
		t.Fatalf("sftp endpoint not reachable at %s; ensure docker-compose is up", addr)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustMarshalPKCS8(t, priv)})
	pubAuth, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}

	sshClient, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer sshClient.Close()

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	defer sftpClient.Close()

	if err := sftpClient.MkdirAll(".ssh"); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	authPath := ".ssh/authorized_keys"
	authFile, err := sftpClient.OpenFile(authPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		t.Fatalf("open authorized_keys: %v", err)
	}
	if _, err := authFile.Write(ssh.MarshalAuthorizedKey(pubAuth)); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	_ = authFile.Close()
	_ = sftpClient.Chmod(authPath, 0o600)

	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, privPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	cfg := filesystem.Config{
		Default: "sftp",
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"sftp": {
				Driver:                    "sftp",
				SFTPHost:                  host,
				SFTPPort:                  port,
				SFTPUser:                  user,
				SFTPKeyPath:               keyFile,
				SFTPInsecureIgnoreHostKey: true,
				Prefix:                    "keyauth",
			},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("SFTP key integration manager init failed: %v", err)
	}
	fs, err := mgr.Disk("sftp")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}
	ctx := context.Background()
	if err := fs.Put(ctx, "file.txt", []byte("hi")); err != nil {
		t.Fatalf("put: %v", err)
	}
	data, err := fs.Get(ctx, "file.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "hi" {
		t.Fatalf("data mismatch: %q", data)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func mustMarshalPKCS8(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return b
}
