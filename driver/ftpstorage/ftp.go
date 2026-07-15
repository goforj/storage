package ftpstorage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/textproto"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/goforj/storage/storagecore"
)

// init registers package integration before callers construct storage.
func init() {
	storagecore.RegisterDriver("ftp", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	mu            sync.Mutex
	conn          ftpConn
	addr          string
	user          string
	pass          string
	prefix        string
	tls           bool
	insecure      bool
	serverName    string
	minTLSVersion uint16
	dialFn        func() (ftpConn, error)
	closed        bool
	closeErr      error
}

type ftpConn interface {
	Login(user, password string) error
	Quit() error
	Retr(path string) (io.ReadCloser, error)
	Stor(path string, reader io.Reader) error
	Delete(path string) error
	RemoveDir(path string) error
	List(path string) ([]*ftp.Entry, error)
	FileSize(path string) (int64, error)
	MakeDir(path string) error
	Rename(from, to string) error
}

type realFTPConn struct {
	conn *ftp.ServerConn
}

// Config defines an FTP-backed storage disk.
// @group Driver Config
//
// Example: define ftp storage config
//
//	cfg := ftpstorage.Config{
//		Host:     "127.0.0.1",
//		User:     "demo",
//		Password: "secret",
//	}
//	_ = cfg
//
// Example: define ftp storage config with all fields
//
//	cfg := ftpstorage.Config{
//		Host:               "127.0.0.1",
//		Port:               21,        // default: 21
//		User:               "demo",    // default: ""
//		Password:           "secret",  // default: ""
//		TLS:                false,     // default: false
//		InsecureSkipVerify: false,     // default: false
//		Prefix:             "uploads", // default: ""
//	}
//	_ = cfg
type Config struct {
	Host               string
	Port               int
	User               string
	Password           string
	TLS                bool
	InsecureSkipVerify bool
	Prefix             string
}

// DriverName returns the registry identifier for FTP storage.
func (Config) DriverName() string { return "ftp" }

// ResolvedConfig maps FTP-specific settings into storagecore's shared configuration.
func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
		Driver:                "ftp",
		FTPHost:               c.Host,
		FTPPort:               c.Port,
		FTPUser:               c.User,
		FTPPassword:           c.Password,
		FTPTLS:                c.TLS,
		FTPInsecureSkipVerify: c.InsecureSkipVerify,
		Prefix:                c.Prefix,
	}
}

// New constructs FTP-backed storage using jlaffaye/ftp.
// @group Driver Constructors
//
// Example: ftp storage
//
//	fs, _ := ftpstorage.New(ftpstorage.Config{
//		Host:     "127.0.0.1",
//		User:     "demo",
//		Password: "secret",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

// NewContext validates cfg and constructs a lazily connected FTP store.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig validates resolved FTP and TLS settings without opening a connection.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.FTPHost == "" {
		return nil, fmt.Errorf("storage: ftp requires FTPHost")
	}
	if !cfg.FTPTLS && cfg.FTPInsecureSkipVerify {
		return nil, fmt.Errorf("storage: ftp TLS options require FTPTLS")
	}
	user := cfg.FTPUser
	pass := cfg.FTPPassword
	port := cfg.FTPPort
	if port == 0 {
		port = 21
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("storage: ftp port must be between 1 and 65535")
	}
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	serverName := ""
	minTLSVersion := uint16(0)
	if cfg.FTPTLS {
		serverName = cfg.FTPHost
		minTLSVersion = tls.VersionTLS12
	}
	addr := net.JoinHostPort(cfg.FTPHost, strconv.Itoa(port))

	return &driver{
		addr:          addr,
		user:          user,
		pass:          pass,
		prefix:        prefix,
		tls:           cfg.FTPTLS,
		insecure:      cfg.FTPInsecureSkipVerify,
		serverName:    serverName,
		minTLSVersion: minTLSVersion,
		dialFn:        nil,
	}, nil
}

// dial opens an FTP control connection with the configured timeout and optional explicit TLS.
func (d *driver) dial() (ftpConn, error) {
	if d.dialFn != nil {
		return d.dialFn()
	}
	opts := []ftp.DialOption{
		ftp.DialWithTimeout(10 * time.Second),
		ftp.DialWithDisabledEPSV(true),
	}
	if d.tls {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{
			MinVersion:         d.minTLSVersion,
			ServerName:         d.serverName,
			InsecureSkipVerify: d.insecure,
		}))
	}
	conn, err := ftp.Dial(d.addr, opts...)
	if err != nil {
		return nil, err
	}
	return realFTPConn{conn: conn}, nil
}

// withConn serializes one FTP command and discards the cached connection after any failure.
func (d *driver) withConn(fn func(ftpConn) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, err := d.ensureConnLocked(); err != nil {
		return err
	}
	if err := d.runConnLocked(fn); err != nil {
		return joinCleanup(err, d.closeConnLocked())
	}
	return nil
}

// withConnRetry retries a transient transport failure once on a freshly authenticated connection.
func (d *driver) withConnRetry(fn func(ftpConn) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, err := d.ensureConnLocked(); err != nil {
		return err
	}
	if err := d.runConnLocked(fn); err != nil {
		if !shouldReconnectFTP(err) {
			return joinCleanup(err, d.closeConnLocked())
		}
		closeErr := d.closeConnLocked()
		if _, retryErr := d.ensureConnLocked(); retryErr != nil {
			return joinCleanup(retryErr, closeErr)
		}
		if retryErr := d.runConnLocked(fn); retryErr != nil {
			return joinCleanup(retryErr, joinCleanup(closeErr, d.closeConnLocked()))
		}
		return nil
	}
	return nil
}

// runConnLocked invokes fn only while the caller holds the connection mutex.
func (d *driver) runConnLocked(fn func(ftpConn) error) error {
	if d.conn == nil {
		return fmt.Errorf("storage: ftp connection unavailable")
	}
	return fn(d.conn)
}

// ensureConnLocked lazily dials and authenticates unless the driver is terminally closed.
func (d *driver) ensureConnLocked() (ftpConn, error) {
	if d.closed {
		return nil, fmt.Errorf("storage: ftp: %w", fs.ErrClosed)
	}
	if d.conn != nil {
		return d.conn, nil
	}
	conn, err := d.dial()
	if err != nil {
		return nil, err
	}
	if d.user != "" || d.pass != "" {
		if err := conn.Login(d.user, d.pass); err != nil {
			return nil, joinCleanup(err, conn.Quit())
		}
	}
	d.conn = conn
	return conn, nil
}

// closeConnLocked quits and forgets the cached FTP connection.
func (d *driver) closeConnLocked() error {
	if d.conn == nil {
		return nil
	}
	err := d.conn.Quit()
	d.conn = nil
	return err
}

// Close prevents future operations and closes the cached FTP connection once.
func (d *driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return d.closeErr
	}
	d.closed = true
	d.closeErr = d.closeConnLocked()
	return d.closeErr
}

// Get retrieves an object using a background context.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext downloads one object, retrying a stale connection before returning any bytes.
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
	var data []byte
	err = d.withConnRetry(func(c ftpConn) error {
		r, err := c.Retr(fp)
		if err != nil {
			return err
		}
		var contents bytes.Buffer
		_, err = copyContext(ctx, &contents, r)
		data = contents.Bytes()
		return joinCleanup(err, r.Close())
	})
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

// Put stores an object using a background context.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir creates a directory chain using a background context.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext creates missing parents and streams an object while honoring cancellation.
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
	return wrapError(d.withConn(func(c ftpConn) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := path.Dir(fp)
		if dir != "" && dir != "." {
			if err := ensureDirs(c, dir); err != nil {
				return err
			}
		}
		return c.Stor(fp, &contextReader{ctx: ctx, reader: bytes.NewReader(contents)})
	}))
}

// MakeDirContext recursively creates a directory and treats the logical root as existing.
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
	return wrapError(d.withConn(func(c ftpConn) error {
		return ensureDirs(c, fp)
	}))
}

// ensureDirs creates each FTP path component and tolerates already-existing directories.
func ensureDirs(c ftpConn, dir string) error {
	parts := strings.Split(dir, "/")
	var cur string
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur = path.Join(cur, p)
		if err := c.MakeDir(cur); err != nil && !isDirectoryExistsError(err) {
			return err
		}
	}
	return nil
}

// Delete removes one object or empty directory using a background context.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext identifies the path type before selecting FTP file or directory removal.
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
	entry, err := d.StatContext(ctx, p)
	if err != nil {
		return err
	}
	return wrapError(d.withConn(func(c ftpConn) error {
		if entry.IsDir {
			return c.RemoveDir(fp)
		}
		return c.Delete(fp)
	}))
}

// Stat inspects a logical path using a background context.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext lists the parent directory because the FTP client has no portable stat command.
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
	var entry storagecore.Entry
	err = d.withConnRetry(func(c ftpConn) error {
		parent := path.Dir(fp)
		if parent == "." {
			parent = ""
		}
		name := path.Base(fp)
		entries, err := c.List(parent)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Name != name {
				continue
			}
			size := int64(e.Size)
			isDir := e.Type == ftp.EntryTypeFolder
			if isDir {
				size = 0
			}
			entry = storagecore.Entry{Path: d.stripPrefix(fp), Size: size, IsDir: isDir}
			return nil
		}
		return &textproto.Error{Code: 550, Msg: "not found"}
	})
	if err != nil {
		return storagecore.Entry{}, wrapError(err)
	}
	return entry, nil
}

// Exists reports object presence using a background context.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext probes file size and reports missing objects without converting other failures.
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
	err = d.withConnRetry(func(c ftpConn) error {
		_, err := c.FileSize(fp)
		return err
	})
	if err != nil {
		wrapped := wrapError(err)
		if errors.Is(wrapped, storagecore.ErrNotFound) {
			return false, nil
		}
		return false, wrapped
	}
	return true, nil
}

// List returns immediate children using a background context.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage paginates immediate children using a background context.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext returns immediate FTP children in deterministic logical-path order.
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
	var entries []storagecore.Entry
	err = d.withConnRetry(func(c ftpConn) error {
		l, err := c.List(fp)
		if err != nil {
			return err
		}
		for _, e := range l {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := e.Name
			if fp != "" && fp != "." && fp != "/" {
				rel = path.Join(d.stripPrefix(fp), e.Name)
			}
			size := int64(e.Size)
			isDir := e.Type == ftp.EntryTypeFolder
			if isDir {
				size = 0
			}
			entries = append(entries, storagecore.Entry{
				Path:  rel,
				Size:  size,
				IsDir: isDir,
			})
		}
		return nil
	})
	if err != nil {
		if d.stripPrefix(fp) == "" && errors.Is(wrapError(err), storagecore.ErrNotFound) {
			return []storagecore.Entry{}, nil
		}
		return nil, wrapError(err)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
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

// Walk traverses a logical subtree using a background context.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext snapshots an FTP subtree before invoking re-entrant user callbacks.
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
	var snapshot []storagecore.Entry
	err = d.withConnRetry(func(c ftpConn) error {
		entries, err := d.walkSnapshot(ctx, c, fp)
		if err == nil {
			snapshot = entries
			return nil
		}
		if wrapped := wrapError(err); !errors.Is(wrapped, storagecore.ErrNotFound) {
			return err
		}
		if d.stripPrefix(fp) == "" {
			snapshot = nil
			return nil
		}

		size, err := c.FileSize(fp)
		if err != nil {
			return err
		}
		snapshot = []storagecore.Entry{{Path: d.stripPrefix(fp), Size: size, IsDir: false}}
		return nil
	})
	if err != nil {
		return wrapError(err)
	}
	slices.SortFunc(snapshot, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, entry := range snapshot {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return nil
}

// Copy duplicates an object using a background context.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext validates both paths, downloads the source, and uploads a distinct destination.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := d.closedError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	srcPath, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	dstPath, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("%w: logical root cannot be used as an object", storagecore.ErrForbidden)
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	if srcPath == dstPath {
		return nil
	}
	return d.PutContext(ctx, dst, data)
}

// Move relocates a path using a background context.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext validates the source, creates destination parents, and uses server-side rename.
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
	return wrapError(d.withConn(func(c ftpConn) error {
		if err := ensureDirs(c, path.Dir(dstPath)); err != nil {
			return err
		}
		return c.Rename(srcPath, dstPath)
	}))
}

// URL reports FTP URL generation as unsupported using a background context.
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
	return "", fmt.Errorf("%w: public URL not supported for ftp", storagecore.ErrUnsupported)
}

// fullPath normalizes a logical path before joining the configured FTP prefix.
func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

// stripPrefix removes only an exact configured path component from FTP names.
func (d *driver) stripPrefix(p string) string {
	if d.prefix == "" {
		return p
	}
	if p == d.prefix {
		return ""
	}
	return strings.TrimPrefix(p, d.prefix+"/")
}

// walkSnapshot reads a complete tree before callbacks run so retrying a stale
// connection cannot replay entries that callers have already observed.
func (d *driver) walkSnapshot(ctx context.Context, c ftpConn, dir string) ([]storagecore.Entry, error) {
	var snapshot []storagecore.Entry
	if err := d.collectWalkEntries(ctx, c, dir, &snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// collectWalkEntries keeps FTP traversal on the serialized connection while
// leaving user callbacks free to re-enter the storage driver.
func (d *driver) collectWalkEntries(ctx context.Context, c ftpConn, dir string, snapshot *[]storagecore.Entry) error {
	entries, err := c.List(dir)
	if err != nil {
		return err
	}
	slices.SortFunc(entries, func(a, b *ftp.Entry) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		full := e.Name
		if dir != "" && dir != "." && dir != "/" {
			full = path.Join(dir, e.Name)
		}
		entry := storagecore.Entry{
			Path:  d.stripPrefix(full),
			Size:  int64(e.Size),
			IsDir: e.Type == ftp.EntryTypeFolder,
		}
		if entry.IsDir {
			entry.Size = 0
		}
		*snapshot = append(*snapshot, entry)
		if entry.IsDir {
			if err := d.collectWalkEntries(ctx, c, full, snapshot); err != nil {
				return err
			}
		}
	}
	return nil
}

// wrapError maps common FTP server text to portable storage error identities.
func wrapError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "access denied") ||
		strings.Contains(msg, "not permitted") ||
		strings.Contains(msg, "not allowed") ||
		strings.Contains(msg, "insufficient privilege") ||
		strings.Contains(msg, "authorization failed") {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	if strings.Contains(msg, "directory not empty") ||
		strings.Contains(msg, "not empty") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "file exists") ||
		strings.Contains(msg, "is a directory") ||
		strings.Contains(msg, "not a directory") {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	if strings.Contains(msg, "not found") || strings.Contains(msg, "not available") || strings.Contains(msg, "no such file") || strings.Contains(msg, "can't check for file existence") || strings.Contains(msg, "550") {
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	}
	return err
}

// isDirectoryExistsError recognizes server replies that make recursive mkdir idempotent.
func isDirectoryExistsError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exist") || strings.Contains(msg, "file exists") || strings.Contains(msg, "directory exists")
}

// joinCleanup preserves the FTP operation error exactly when connection cleanup succeeds.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// closedError rejects work after Close while synchronizing access to terminal state.
func (d *driver) closedError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fmt.Errorf("storage: ftp: %w", fs.ErrClosed)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Read stops an upload stream before its next read when the context is canceled.
func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// copyContext copies a download while checking cancellation between read cycles.
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

// shouldReconnectFTP limits retries to transport and transient FTP data-channel failures.
func shouldReconnectFTP(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, storagecore.ErrNotFound) {
		return false
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Code == 421 || protoErr.Code == 425 || protoErr.Code == 426
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "closed network connection") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection aborted") ||
		strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "use of closed network connection")
}

// Login authenticates the wrapped goftp connection.
func (c realFTPConn) Login(user, password string) error {
	return c.conn.Login(user, password)
}

// Quit closes the wrapped goftp connection cleanly.
func (c realFTPConn) Quit() error {
	return c.conn.Quit()
}

// Retr opens a download stream on the wrapped goftp connection.
func (c realFTPConn) Retr(path string) (io.ReadCloser, error) {
	return c.conn.Retr(path)
}

// Stor uploads a stream through the wrapped goftp connection.
func (c realFTPConn) Stor(path string, reader io.Reader) error {
	return c.conn.Stor(path, reader)
}

// Delete removes a file through the wrapped goftp connection.
func (c realFTPConn) Delete(path string) error {
	return c.conn.Delete(path)
}

// RemoveDir removes an empty directory through the wrapped goftp connection.
func (c realFTPConn) RemoveDir(path string) error {
	return c.conn.RemoveDir(path)
}

// List returns directory entries from the wrapped goftp connection.
func (c realFTPConn) List(path string) ([]*ftp.Entry, error) {
	return c.conn.List(path)
}

// FileSize probes an object's byte length through the wrapped goftp connection.
func (c realFTPConn) FileSize(path string) (int64, error) {
	return c.conn.FileSize(path)
}

// MakeDir creates one directory through the wrapped goftp connection.
func (c realFTPConn) MakeDir(path string) error {
	return c.conn.MakeDir(path)
}

// Rename relocates a path through the wrapped goftp connection.
func (c realFTPConn) Rename(from, to string) error {
	return c.conn.Rename(from, to)
}
