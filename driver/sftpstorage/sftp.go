package sftpstorage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/goforj/storage"
)

func init() {
	storage.RegisterDriver("sftp", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client *sftp.Client
	prefix string
}

// Config defines an SFTP-backed storage disk.
// @group Drivers
//
// Example: define sftp storage config
//
//	cfg := sftpstorage.Config{
//		Host:     "127.0.0.1",
//		User:     "demo",
//		Password: "secret",
//	}
//	_ = cfg
type Config struct {
	Host                  string
	Port                  int
	User                  string
	Password              string
	KeyPath               string
	KnownHostsPath        string
	InsecureIgnoreHostKey bool
	Prefix                string
}

func (Config) DriverName() string { return "sftp" }

func (c Config) ResolvedConfig() storage.ResolvedConfig {
	return storage.ResolvedConfig{
		Driver:                    "sftp",
		SFTPHost:                  c.Host,
		SFTPPort:                  c.Port,
		SFTPUser:                  c.User,
		SFTPPassword:              c.Password,
		SFTPKeyPath:               c.KeyPath,
		SFTPKnownHostsPath:        c.KnownHostsPath,
		SFTPInsecureIgnoreHostKey: c.InsecureIgnoreHostKey,
		Prefix:                    c.Prefix,
	}
}

// New constructs SFTP-backed storage using ssh and pkg/sftp.
// @group Drivers
//
// Example: sftp storage
//
//	fs, _ := sftpstorage.New(context.Background(), sftpstorage.Config{
//		Host:     "127.0.0.1",
//		User:     "demo",
//		Password: "secret",
//	})
//	_ = fs
func New(ctx context.Context, cfg Config) (storage.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(_ context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	if cfg.SFTPHost == "" {
		return nil, fmt.Errorf("storage: sftp storage requires SFTPHost")
	}
	user := cfg.SFTPUser
	if user == "" {
		user = "root"
	}
	port := cfg.SFTPPort
	if port == 0 {
		port = 22
	}

	authMethods, err := buildAuth(cfg)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := buildHostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", cfg.SFTPHost, port)
	sshClient, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("storage: sftp dial: %w", err)
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, fmt.Errorf("storage: sftp client: %w", err)
	}

	prefix, err := storage.NormalizePath(cfg.Prefix)
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	return &driver{
		client: client,
		prefix: prefix,
	}, nil
}

func buildAuth(cfg storage.ResolvedConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if cfg.SFTPPassword != "" {
		methods = append(methods, ssh.Password(cfg.SFTPPassword))
	}
	if cfg.SFTPKeyPath != "" {
		key, err := os.ReadFile(cfg.SFTPKeyPath)
		if err != nil {
			return nil, fmt.Errorf("storage: read key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("storage: parse key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("storage: sftp requires password or key")
	}
	return methods, nil
}

func buildHostKeyCallback(cfg storage.ResolvedConfig) (ssh.HostKeyCallback, error) {
	if cfg.SFTPInsecureIgnoreHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	if cfg.SFTPKnownHostsPath != "" {
		return knownhosts.New(cfg.SFTPKnownHostsPath)
	}
	// Default to insecure if nothing provided to keep behavior predictable.
	return ssh.InsecureIgnoreHostKey(), nil
}

func (d *driver) Get(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	f, err := d.client.Open(fp)
	if err != nil {
		return nil, wrapError(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func (d *driver) Put(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if err := d.client.MkdirAll(path.Dir(fp)); err != nil {
		return wrapError(err)
	}
	f, err := d.client.OpenFile(fp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	if err != nil {
		return wrapError(err)
	}
	defer f.Close()
	if _, err := f.Write(contents); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *driver) Delete(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if err := d.client.Remove(fp); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *driver) Exists(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return false, err
	}
	info, err := d.client.Stat(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, wrapError(err)
	}
	if info.IsDir() {
		return false, nil
	}
	return true, nil
}

func (d *driver) List(ctx context.Context, p string) ([]storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	infos, err := d.client.ReadDir(fp)
	if err != nil {
		return nil, wrapError(err)
	}
	basePrefix := d.stripPrefix(fp)
	var entries []storage.Entry
	for _, info := range infos {
		rel := path.Join(basePrefix, info.Name())
		entries = append(entries, storage.Entry{
			Path:  rel,
			Size:  info.Size(),
			IsDir: info.IsDir(),
		})
	}
	return entries, nil
}

func (d *driver) URL(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: public URL not supported for sftp", storage.ErrUnsupported)
}

func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storage.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storage.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) stripPrefix(p string) string {
	if d.prefix == "" {
		return p
	}
	trimmed := strings.TrimPrefix(p, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func wrapError(err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
	}
	if os.IsPermission(err) {
		return fmt.Errorf("%w: %v", storage.ErrForbidden, err)
	}
	return err
}
