//go:build integration

package rclone

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/goforj/filesystem"
	filesystemtest "github.com/goforj/filesystem/testutil"
)

func TestRcloneSFTPKeyIntegration(t *testing.T) {
	filesystemtest.RequireIntegration(t)

	host := filesystemtest.GetenvDefault("INTEGRATION_SFTP_HOST", "127.0.0.1")
	portStr := filesystemtest.GetenvDefault("INTEGRATION_SFTP_PORT", "2222")
	user := filesystemtest.GetenvDefault("INTEGRATION_SFTP_USER", "fsuser")
	pass := filesystemtest.GetenvDefault("INTEGRATION_SFTP_PASS", "fspass")
	addr := fmt.Sprintf("%s:%s", host, portStr)
	if !filesystemtest.Reachable(addr) {
		t.Fatalf("sftp endpoint not reachable at %s", addr)
	}

	// Generate key pair for the test.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustMarshalPKCS8(t, priv)})
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, privPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	pubAuth, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}

	var hostKey ssh.PublicKey
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostKey = key
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
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

	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	entry := fmt.Sprintf("[%s]:%s %s", host, portStr, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))))
	if err := os.WriteFile(knownHosts, []byte(entry+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	inline := fmt.Sprintf(`
[sftpkey]
type = sftp
host = %s
port = %s
user = %s
key_file = %s
known_hosts_file = %s
md5sum_command =
sha1sum_command =
`, host, portStr, user, keyFile, knownHosts)

	_ = setRcloneConfigData(inline)

	cfg := filesystem.Config{
		Default:          "rclone",
		RcloneConfigData: inline,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rclone": {Driver: "rclone", Remote: "sftpkey:/config", Prefix: "keyauth"},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		t.Fatalf("rclone sftp key integration manager init failed: %v", err)
	}
	fs, err := mgr.Disk("rclone")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}

	ctx := context.Background()
	if err := fs.Put(ctx, "file.txt", []byte("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	data, err := fs.Get(ctx, "file.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data mismatch: %q", data)
	}
}

func mustMarshalPKCS8(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return b
}
