//go:build integration

package sftpdriver

import (
	"fmt"
	"os"
	"strconv"
	"testing"

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
