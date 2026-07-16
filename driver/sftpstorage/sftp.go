package sftpstorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/goforj/storage/storagecore"
)

var sshDial = ssh.Dial
var closeSSHClient = func(client *ssh.Client) error { return client.Close() }

var newSFTPClient = func(sshClient *ssh.Client) (sftpClient, error) {
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, err
	}
	return &realSFTPClient{client: client, sshClient: sshClient}, nil
}

// init registers package integration before callers construct storage.
func init() {
	storagecore.RegisterDriver("sftp", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client    sftpClient
	prefix    string
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

type sftpClient interface {
	Open(path string) (io.ReadCloser, error)
	OpenFile(path string, flags int) (io.WriteCloser, error)
	MkdirAll(path string) error
	Remove(path string) error
	RemoveDirectory(path string) error
	PosixRename(oldname, newname string) error
	Stat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.FileInfo, error)
	Close() error
}

type realSFTPClient struct {
	client    *sftp.Client
	sshClient *ssh.Client
	closeOnce sync.Once
	closeErr  error
}

// Config defines an SFTP-backed storage disk.
// @group Driver Config
//
// Example: define sftp storage config
//
//	cfg := sftpstorage.Config{
//		Host:     "127.0.0.1",
//		User:     "demo",
//		Password: "secret",
//	}
//	_ = cfg
//
// Example: define sftp storage config with all fields
//
//	cfg := sftpstorage.Config{
//		Host:                  "127.0.0.1",
//		Port:                  22,            // default: 22
//		User:                  "demo",        // default: "root"
//		Password:              "secret",      // default: ""
//		KeyPath:               "/path/id_ed25519",      // default: ""
//		KnownHostsPath:        "/path/known_hosts",     // default: ""
//		InsecureIgnoreHostKey: false,         // default: false
//		Prefix:                "uploads",     // default: ""
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

// DriverName returns the registry identifier for SFTP storage.
func (Config) DriverName() string { return "sftp" }

// ResolvedConfig maps SSH, authentication, and prefix settings into storagecore.
func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
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
// @group Driver Constructors
//
// Example: sftp storage
//
//	fs, _ := sftpstorage.New(sftpstorage.Config{
//		Host:     "127.0.0.1",
//		User:     "demo",
//		Password: "secret",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

// NewContext validates cfg and establishes an SFTP session.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig validates authentication and host verification before dialing SSH.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.SFTPHost == "" {
		return nil, fmt.Errorf("storage: sftp storage requires SFTPHost")
	}
	if cfg.SFTPInsecureIgnoreHostKey && cfg.SFTPKnownHostsPath != "" {
		return nil, fmt.Errorf("storage: sftp KnownHostsPath and InsecureIgnoreHostKey are mutually exclusive")
	}
	user := cfg.SFTPUser
	if user == "" {
		user = "root"
	}
	port := cfg.SFTPPort
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("storage: sftp port must be between 1 and 65535")
	}
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
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

	addr := net.JoinHostPort(cfg.SFTPHost, strconv.Itoa(port))
	sshClient, err := sshDial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("storage: sftp dial: %w", err)
	}
	client, err := newSFTPClient(sshClient)
	if err != nil {
		return nil, joinCleanup(fmt.Errorf("storage: sftp client: %w", err), closeSSHClient(sshClient))
	}

	return &driver{
		client: client,
		prefix: prefix,
	}, nil
}

// buildAuth assembles password and private-key methods and rejects anonymous SFTP access.
func buildAuth(cfg storagecore.ResolvedConfig) ([]ssh.AuthMethod, error) {
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

// buildHostKeyCallback requires known-host verification unless insecurity is explicitly enabled.
func buildHostKeyCallback(cfg storagecore.ResolvedConfig) (ssh.HostKeyCallback, error) {
	if cfg.SFTPInsecureIgnoreHostKey && cfg.SFTPKnownHostsPath != "" {
		return nil, fmt.Errorf("storage: sftp KnownHostsPath and InsecureIgnoreHostKey are mutually exclusive")
	}
	if cfg.SFTPInsecureIgnoreHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	if cfg.SFTPKnownHostsPath != "" {
		return knownhosts.New(cfg.SFTPKnownHostsPath)
	}
	return nil, fmt.Errorf("storage: sftp requires KnownHostsPath unless InsecureIgnoreHostKey is explicitly enabled")
}

// Get retrieves an object using a background context.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext reads one remote file and preserves both transfer and close failures.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	if d.stripPrefix(fp) == "" {
		return nil, fmt.Errorf("%w: logical root cannot be read as an object", storagecore.ErrForbidden)
	}
	f, err := d.client.Open(fp)
	if err != nil {
		return nil, wrapError(err)
	}
	var data strings.Builder
	_, err = copyContext(ctx, &data, f)
	closeErr := f.Close()
	if err != nil {
		return nil, joinCleanup(wrapError(err), wrapError(closeErr))
	}
	if closeErr != nil {
		return nil, wrapError(closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte(data.String()), nil
}

// Put stores an object using a background context.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir creates a remote directory using a background context.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext writes a unique sibling temporary and atomically renames it over the destination.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(fp) == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	if err := d.client.MkdirAll(path.Dir(fp)); err != nil {
		return wrapError(err)
	}
	temporary, err := temporaryName(fp)
	if err != nil {
		return err
	}
	f, err := d.client.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return wrapError(err)
	}
	for len(contents) > 0 {
		if err := ctx.Err(); err != nil {
			return d.discardTemporary(temporary, f, err)
		}
		written, err := f.Write(contents)
		if err != nil {
			return d.discardTemporary(temporary, f, wrapError(err))
		}
		if written == 0 {
			return d.discardTemporary(temporary, f, io.ErrShortWrite)
		}
		contents = contents[written:]
	}
	if err := f.Close(); err != nil {
		return d.discardTemporary(temporary, nil, wrapError(err))
	}
	if err := ctx.Err(); err != nil {
		return d.discardTemporary(temporary, nil, err)
	}
	if err := d.client.PosixRename(temporary, fp); err != nil {
		return d.discardTemporary(temporary, nil, wrapError(err))
	}
	return nil
}

// MakeDirContext recursively creates a remote directory and treats the logical root as existing.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(fp) == "" {
		return nil
	}
	if err := d.client.MkdirAll(fp); err != nil {
		return wrapError(err)
	}
	return nil
}

// Delete removes one file or empty directory using a background context.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext stats the path before selecting non-recursive file or directory removal.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(fp) == "" {
		return fmt.Errorf("%w: logical root cannot be deleted", storagecore.ErrForbidden)
	}
	info, err := d.client.Stat(fp)
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return wrapError(err)
	}
	if info.IsDir() {
		err = d.client.RemoveDirectory(fp)
	} else {
		err = d.client.Remove(fp)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return wrapError(err)
	}
	return nil
}

// Stat inspects a logical SFTP path using a background context.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext converts remote file metadata to the storage entry contract.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return storagecore.Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if d.stripPrefix(fp) == "" {
		return storagecore.Entry{Path: "", IsDir: true}, nil
	}
	info, err := d.client.Stat(fp)
	if err != nil {
		return storagecore.Entry{}, wrapError(err)
	}
	size := info.Size()
	if info.IsDir() {
		size = 0
	}
	return storagecore.Entry{Path: d.stripPrefix(fp), Size: size, IsDir: info.IsDir()}, nil
}

// Exists reports file presence using a background context.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext reports concrete files while treating directories and missing paths as absent.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return false, err
	}
	if d.stripPrefix(fp) == "" {
		return false, nil
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

// List returns immediate SFTP children using a background context.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage paginates immediate SFTP children using a background context.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext returns sorted immediate remote children with directory sizes normalized to zero.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	infos, err := d.client.ReadDir(fp)
	if err != nil {
		if d.stripPrefix(fp) == "" {
			if errors.Is(wrapError(err), storagecore.ErrNotFound) {
				return []storagecore.Entry{}, nil
			}
			info, statErr := d.client.Stat(fp)
			if statErr == nil && !info.IsDir() {
				return []storagecore.Entry{}, nil
			}
		}
		return nil, wrapError(err)
	}
	slices.SortFunc(infos, func(a, b os.FileInfo) int {
		return strings.Compare(a.Name(), b.Name())
	})
	basePrefix := d.stripPrefix(fp)
	var entries []storagecore.Entry
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel := path.Join(basePrefix, info.Name())
		size := info.Size()
		if info.IsDir() {
			size = 0
		}
		entries = append(entries, storagecore.Entry{
			Path:  rel,
			Size:  size,
			IsDir: info.IsDir(),
		})
	}
	return entries, nil
}

// ListPageContext paginates the deterministic snapshot returned by ListContext.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

// Walk traverses a logical SFTP subtree using a background context.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext distinguishes a file root from a directory before recursive traversal.
func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("%w: walk callback is required", storagecore.ErrForbidden)
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	if d.stripPrefix(fp) == "" {
		err := d.walkDir(ctx, fp, fn)
		if errors.Is(err, storagecore.ErrNotFound) {
			return nil
		}
		if err != nil {
			info, statErr := d.client.Stat(fp)
			if statErr == nil && !info.IsDir() {
				return nil
			}
		}
		return err
	}
	info, err := d.client.Stat(fp)
	if err != nil {
		return wrapError(err)
	}
	if !info.IsDir() {
		return fn(storagecore.Entry{Path: d.stripPrefix(fp), Size: info.Size(), IsDir: false})
	}
	return d.walkDir(ctx, fp, fn)
}

// Copy duplicates an object using a background context.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext validates both paths and preserves a normalized same-path object unchanged.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dstPath, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if dstPath == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	srcPath, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	if srcPath == dstPath {
		return nil
	}
	return d.PutContext(ctx, dst, data)
}

// Move relocates a remote path using a background context.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext validates the source, creates destination parents, and uses POSIX rename.
func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	srcLogical, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	dstLogical, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if srcLogical == "" || dstLogical == "" {
		return fmt.Errorf("%w: logical root cannot be moved", storagecore.ErrForbidden)
	}
	if _, err := d.StatContext(ctx, src); err != nil {
		return err
	}
	if srcLogical == dstLogical {
		return nil
	}
	srcPath := storagecore.JoinPrefix(d.prefix, srcLogical)
	dstPath := storagecore.JoinPrefix(d.prefix, dstLogical)
	if err := d.client.MkdirAll(path.Dir(dstPath)); err != nil {
		return wrapError(err)
	}
	if err := d.client.PosixRename(srcPath, dstPath); err != nil {
		return wrapError(err)
	}
	return nil
}

// URL reports SFTP URL generation as unsupported using a background context.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext honors closure and cancellation before reporting unsupported URL generation.
func (d *driver) URLContext(ctx context.Context, _ string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%w: public URL not supported for sftp", storagecore.ErrUnsupported)
}

// fullPath normalizes a logical path before joining the configured SFTP prefix.
func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// stripPrefix removes only an exact configured component from a remote path.
func (d *driver) stripPrefix(p string) string {
	if d.prefix == "" {
		return p
	}
	if p == d.prefix {
		return ""
	}
	return strings.TrimPrefix(p, d.prefix+"/")
}

// temporaryName creates an unpredictable sibling so failed writes never
// truncate the live SFTP destination.
func temporaryName(target string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("storage: sftp temporary name: %w", err)
	}
	return path.Join(path.Dir(target), ".storage-tmp-"+hex.EncodeToString(random[:])), nil
}

// discardTemporary closes and removes a partial upload while preserving the
// triggering error exactly when cleanup succeeds.
func (d *driver) discardTemporary(name string, writer io.Closer, operationErr error) error {
	var cleanupErr error
	if writer != nil {
		cleanupErr = wrapError(writer.Close())
	}
	removeErr := d.client.Remove(name)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	cleanupErr = joinCleanup(cleanupErr, wrapError(removeErr))
	return joinCleanup(operationErr, cleanupErr)
}

// walkDir traverses remote entries depth-first in deterministic sibling order.
func (d *driver) walkDir(ctx context.Context, dir string, fn func(storagecore.Entry) error) error {
	infos, err := d.client.ReadDir(dir)
	if err != nil {
		return wrapError(err)
	}
	slices.SortFunc(infos, func(a, b os.FileInfo) int {
		return strings.Compare(a.Name(), b.Name())
	})
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := d.stripPrefix(path.Join(dir, info.Name()))
		entry := storagecore.Entry{Path: rel, Size: info.Size(), IsDir: info.IsDir()}
		if entry.IsDir {
			entry.Size = 0
		}
		if err := fn(entry); err != nil {
			return err
		}
		if info.IsDir() {
			if err := d.walkDir(ctx, path.Join(dir, info.Name()), fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// wrapError maps filesystem absence, permission, and non-empty errors to storage identities.
func wrapError(err error) error {
	if err == nil {
		return nil
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	}
	if os.IsPermission(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	if errors.Is(err, syscall.ENOTEMPTY) || strings.Contains(strings.ToLower(err.Error()), "not empty") {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	return err
}

// copyContext checks cancellation between bounded SFTP reads.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

// joinCleanup preserves a primary SFTP error exactly when closing succeeds.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// Close releases the SFTP session and its owning SSH transport once.
func (d *driver) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		if d.client != nil {
			d.closeErr = d.client.Close()
		}
	})
	return d.closeErr
}

// closedError prevents new work after transport ownership has been released.
func (d *driver) closedError() error {
	if d.closed.Load() {
		return fmt.Errorf("storage: sftp: %w", fs.ErrClosed)
	}
	return nil
}

// Open adapts an SFTP download to the driver's reader interface.
func (c *realSFTPClient) Open(path string) (io.ReadCloser, error) {
	return c.client.Open(path)
}

// OpenFile opens a remote upload target with the requested POSIX flags.
func (c *realSFTPClient) OpenFile(path string, flags int) (io.WriteCloser, error) {
	return c.client.OpenFile(path, flags)
}

// MkdirAll recursively creates a path through the concrete SFTP client.
func (c *realSFTPClient) MkdirAll(path string) error {
	return c.client.MkdirAll(path)
}

// Remove deletes one file through the concrete SFTP client.
func (c *realSFTPClient) Remove(path string) error {
	return c.client.Remove(path)
}

// RemoveDirectory delegates directory deletion without falling back to recursive removal.
func (c *realSFTPClient) RemoveDirectory(path string) error {
	return c.client.RemoveDirectory(path)
}

// PosixRename requests the server's atomic overwrite extension without fallback.
func (c *realSFTPClient) PosixRename(oldname, newname string) error {
	return c.client.PosixRename(oldname, newname)
}

// Stat retrieves remote metadata through the concrete SFTP client.
func (c *realSFTPClient) Stat(path string) (os.FileInfo, error) {
	return c.client.Stat(path)
}

// ReadDir retrieves immediate children through the concrete SFTP client.
func (c *realSFTPClient) ReadDir(path string) ([]os.FileInfo, error) {
	return c.client.ReadDir(path)
}

// Close releases both SFTP and owning SSH clients once while retaining all failures.
func (c *realSFTPClient) Close() error {
	c.closeOnce.Do(func() {
		var errs []error
		if c.client != nil {
			if err := c.client.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if c.sshClient != nil {
			if err := c.sshClient.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}
