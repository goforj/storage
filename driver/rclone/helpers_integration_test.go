//go:build integration

package rclone

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	filesystemtest "github.com/goforj/filesystem/testutil"
	"github.com/goftp/server"
	"github.com/rclone/rclone/fs/config/obscure"
)

type rcloneRemotes struct {
	inline      string
	minioRemote string
	gcsRemote   string
	ftpRemote   string
	sftpRemote  string
}

// hostPort extracts host:port from an endpoint URL (http://host:port).
func hostPort(endpoint string) string {
	trim := strings.TrimPrefix(endpoint, "http://")
	trim = strings.TrimPrefix(trim, "https://")
	return trim
}

// ensureRcloneConfig builds a composite inline config once and returns remote names.
func ensureRcloneConfig(t *testing.T) rcloneRemotes {
	t.Helper()
	var res rcloneRemotes
	var sb strings.Builder
	ctx := context.Background()

	// MinIO/S3
	if endpoint, region, access, secret, bucket := filesystemtest.S3Settings(); filesystemtest.Reachable(hostPort(endpoint)) {
		if err := filesystemtest.EnsureS3Bucket(ctx, endpoint, region, access, secret, bucket); err == nil {
			sb.WriteString(fmt.Sprintf(`
[minio]
type = s3
provider = Minio
access_key_id = %s
secret_access_key = %s
region = %s
endpoint = %s
force_path_style = true
`, access, secret, region, endpoint))
			res.minioRemote = fmt.Sprintf("minio:%s", bucket)
		}
	}

	// GCS (requires creds JSON)
	if creds := os.Getenv("INTEGRATION_GCS_CREDS_JSON"); creds != "" {
		if endpoint, bucket := filesystemtest.GCSSettings(); filesystemtest.Reachable(hostPort(endpoint)) {
			if err := filesystemtest.EnsureGCSBucket(ctx, endpoint, bucket); err == nil {
				sb.WriteString(fmt.Sprintf(`
[fakegcs]
type = googlecloudstorage
service_account_credentials = %s
endpoint = %s
`, creds, endpoint))
				res.gcsRemote = fmt.Sprintf("fakegcs:%s", bucket)
			}
		}
	}

	// FTP
	host := filesystemtest.GetenvDefault("INTEGRATION_FTP_HOST", "127.0.0.1")
	port := filesystemtest.GetenvDefault("INTEGRATION_FTP_PORT", "2121")
	user := filesystemtest.GetenvDefault("INTEGRATION_FTP_USER", "ftpuser")
	pass := filesystemtest.GetenvDefault("INTEGRATION_FTP_PASS", "ftppass")
	if addr := fmt.Sprintf("%s:%s", host, port); !filesystemtest.Reachable(addr) {
		host, port, user, pass = startEmbeddedFTP(t)
	}
	if addr := fmt.Sprintf("%s:%s", host, port); filesystemtest.Reachable(addr) {
		sb.WriteString(fmt.Sprintf(`
[ftpbackend]
type = ftp
host = %s
port = %s
user = %s
pass = %s
`, host, port, user, obscure.MustObscure(pass)))
		res.ftpRemote = "ftpbackend:/"
	}

	// SFTP
	shost := filesystemtest.GetenvDefault("INTEGRATION_SFTP_HOST", "127.0.0.1")
	sport := filesystemtest.GetenvDefault("INTEGRATION_SFTP_PORT", "2222")
	suser := filesystemtest.GetenvDefault("INTEGRATION_SFTP_USER", "fsuser")
	spass := obscure.MustObscure(filesystemtest.GetenvDefault("INTEGRATION_SFTP_PASS", "fspass"))
	if addr := fmt.Sprintf("%s:%s", shost, sport); filesystemtest.Reachable(addr) {
		sb.WriteString(fmt.Sprintf(`
[sftpbackend]
type = sftp
host = %s
port = %s
user = %s
pass = %s
md5sum_command =
sha1sum_command =
`, shost, sport, suser, spass))
		res.sftpRemote = "sftpbackend:/config"
	}

	res.inline = sb.String()
	if res.inline != "" {
		_ = setRcloneConfigData(res.inline)
	}
	return res
}

func startEmbeddedFTP(t *testing.T) (host, port, user, pass string) {
	t.Helper()
	root := t.TempDir()
	host = "127.0.0.1"
	user, pass = "ftpuser", "ftppass"

	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("ftp listen: %v", err)
	}
	port = fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()

	opts := &server.ServerOpts{
		Factory:  &memFactory{root: root},
		Hostname: host,
		Port:     atoi(port),
		Auth:     &server.SimpleAuth{Name: user, Password: pass},
	}
	s := server.NewServer(opts)
	go func() { _ = s.ListenAndServe() }()
	t.Cleanup(func() { _ = s.Shutdown() })
	time.Sleep(200 * time.Millisecond)
	return
}

type memFactory struct {
	root string
}

func (f *memFactory) NewDriver() (server.Driver, error) {
	return &memDriver{root: f.root, perm: server.NewSimplePerm("user", "group")}, nil
}

type memDriver struct {
	root string
	perm server.Perm
}

func (d *memDriver) Init(*server.Conn) {}

func (d *memDriver) Stat(p string) (server.FileInfo, error) {
	fi, err := os.Stat(d.abs(p))
	if err != nil {
		return nil, err
	}
	return fileInfo{FileInfo: fi}, nil
}

func (d *memDriver) ChangeDir(p string) error {
	fi, err := os.Stat(d.abs(p))
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return os.ErrInvalid
	}
	return nil
}

func (d *memDriver) ListDir(p string, cb func(server.FileInfo) error) error {
	dir := d.abs(p)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := cb(fileInfo{FileInfo: info}); err != nil {
			return err
		}
	}
	return nil
}

func (d *memDriver) DeleteDir(p string) error  { return os.RemoveAll(d.abs(p)) }
func (d *memDriver) DeleteFile(p string) error { return os.Remove(d.abs(p)) }
func (d *memDriver) Rename(from, to string) error {
	return os.Rename(d.abs(from), d.abs(to))
}
func (d *memDriver) MakeDir(p string) error {
	return os.MkdirAll(d.abs(p), 0o755)
}
func (d *memDriver) GetFile(p string, _ int64) (int64, io.ReadCloser, error) {
	f, err := os.Open(d.abs(p))
	if err != nil {
		return 0, nil, err
	}
	info, _ := f.Stat()
	return info.Size(), f, nil
}
func (d *memDriver) PutFile(p string, r io.Reader, _ bool) (int64, error) {
	fp := d.abs(p)
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(fp)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

func (d *memDriver) abs(p string) string {
	if p == "" || p == "." || p == "/" {
		return d.root
	}
	return filepath.Join(d.root, p)
}

type fileInfo struct {
	os.FileInfo
}

func (f fileInfo) Owner() string { return "user" }
func (f fileInfo) Group() string { return "group" }

func atoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}
