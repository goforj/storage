//go:build integration

package rclone

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	filesystemtest "github.com/goforj/filesystem/testutil"
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
	pass := obscure.MustObscure(filesystemtest.GetenvDefault("INTEGRATION_FTP_PASS", "ftppass"))
	if addr := fmt.Sprintf("%s:%s", host, port); filesystemtest.Reachable(addr) {
		sb.WriteString(fmt.Sprintf(`
[ftpbackend]
type = ftp
host = %s
port = %s
user = %s
pass = %s
`, host, port, user, pass))
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
		res.sftpRemote = "sftpbackend:/home/fsuser"
	}

	res.inline = sb.String()
	if res.inline != "" {
		_ = setRcloneConfigData(res.inline)
	}
	return res
}
