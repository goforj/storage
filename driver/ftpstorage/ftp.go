package ftpstorage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/goforj/storage/storagecore"
)

func init() {
	storagecore.RegisterDriver("ftp", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	mu       sync.Mutex
	conn     ftpConn
	addr     string
	user     string
	pass     string
	prefix   string
	tls      bool
	insecure bool
	dialFn   func() (ftpConn, error)
}

type ftpConn interface {
	Login(user, password string) error
	Quit() error
	Retr(path string) (io.ReadCloser, error)
	Stor(path string, reader io.Reader) error
	Delete(path string) error
	List(path string) ([]*ftp.Entry, error)
	FileSize(path string) (int64, error)
	MakeDir(path string) error
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

func (Config) DriverName() string { return "ftp" }

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

func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(_ context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	if cfg.FTPHost == "" {
		return nil, fmt.Errorf("storage: ftp requires FTPHost")
	}
	user := cfg.FTPUser
	pass := cfg.FTPPassword
	port := cfg.FTPPort
	if port == 0 {
		port = 21
	}
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("%s:%d", cfg.FTPHost, port)

	return &driver{
		addr:     addr,
		user:     user,
		pass:     pass,
		prefix:   prefix,
		tls:      cfg.FTPTLS,
		insecure: cfg.FTPInsecureSkipVerify,
		dialFn:   nil,
	}, nil
}

func (d *driver) dial() (ftpConn, error) {
	if d.dialFn != nil {
		return d.dialFn()
	}
	opts := []ftp.DialOption{
		ftp.DialWithTimeout(10 * time.Second),
		ftp.DialWithDisabledEPSV(true),
	}
	if d.tls {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{InsecureSkipVerify: d.insecure}))
	}
	conn, err := ftp.Dial(d.addr, opts...)
	if err != nil {
		return nil, err
	}
	return realFTPConn{conn: conn}, nil
}

func (d *driver) withConn(fn func(ftpConn) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, err := d.ensureConnLocked(); err != nil {
		return err
	}
	if err := d.runConnLocked(fn); err != nil {
		if !shouldReconnectFTP(err) {
			d.closeConnLocked()
			return err
		}
		d.closeConnLocked()
		if _, retryErr := d.ensureConnLocked(); retryErr != nil {
			return retryErr
		}
		if retryErr := d.runConnLocked(fn); retryErr != nil {
			d.closeConnLocked()
			return retryErr
		}
	}
	return nil
}

func (d *driver) runConnLocked(fn func(ftpConn) error) error {
	if d.conn == nil {
		return fmt.Errorf("storage: ftp connection unavailable")
	}
	return fn(d.conn)
}

func (d *driver) ensureConnLocked() (ftpConn, error) {
	if d.conn != nil {
		return d.conn, nil
	}
	conn, err := d.dial()
	if err != nil {
		return nil, err
	}
	if d.user != "" || d.pass != "" {
		if err := conn.Login(d.user, d.pass); err != nil {
			_ = conn.Quit()
			return nil, err
		}
	}
	d.conn = conn
	return conn, nil
}

func (d *driver) closeConnLocked() {
	if d.conn == nil {
		return
	}
	_ = d.conn.Quit()
	d.conn = nil
}

func (d *driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeConnLocked()
	return nil
}

func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = d.withConn(func(c ftpConn) error {
		r, err := c.Retr(fp)
		if err != nil {
			return err
		}
		defer r.Close()
		data, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	return wrapError(d.withConn(func(c ftpConn) error {
		dir := path.Dir(fp)
		if dir != "" && dir != "." {
			_ = ensureDirs(c, dir)
		}
		return c.Stor(fp, bytes.NewReader(contents))
	}))
}

func ensureDirs(c ftpConn, dir string) error {
	parts := strings.Split(dir, "/")
	var cur string
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur = path.Join(cur, p)
		_ = c.MakeDir(cur)
	}
	return nil
}

func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

func (d *driver) DeleteContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	return wrapError(d.withConn(func(c ftpConn) error {
		return c.Delete(fp)
	}))
}

func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	var entry storagecore.Entry
	err = d.withConn(func(c ftpConn) error {
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

func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return false, err
	}
	err = d.withConn(func(c ftpConn) error {
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

func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	var entries []storagecore.Entry
	err = d.withConn(func(c ftpConn) error {
		l, err := c.List(fp)
		if err != nil {
			return err
		}
		for _, e := range l {
			rel := e.Name
			if fp != "" && fp != "." && fp != "/" {
				rel = path.Join(d.stripPrefix(fp), e.Name)
			}
			entries = append(entries, storagecore.Entry{
				Path:  rel,
				Size:  int64(e.Size),
				IsDir: e.Type == ftp.EntryTypeFolder,
			})
		}
		return nil
	})
	if err != nil {
		return nil, wrapError(err)
	}
	return entries, nil
}

func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	return wrapError(d.withConn(func(c ftpConn) error {
		if err := d.walkDir(ctx, c, fp, fn); err == nil {
			return nil
		} else if wrapped := wrapError(err); !errors.Is(wrapped, storagecore.ErrNotFound) {
			return err
		}

		size, err := c.FileSize(fp)
		if err != nil {
			return err
		}
		return fn(storagecore.Entry{Path: d.stripPrefix(fp), Size: size, IsDir: false})
	}))
}

func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	return d.PutContext(ctx, dst, data)
}

func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	if err := d.CopyContext(ctx, src, dst); err != nil {
		return err
	}
	return d.DeleteContext(ctx, src)
}

func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

func (d *driver) URLContext(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: public URL not supported for ftp", storagecore.ErrUnsupported)
}

func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return storagecore.JoinPrefix(d.prefix, normalized), nil
}

func (d *driver) stripPrefix(p string) string {
	if d.prefix == "" {
		return p
	}
	trimmed := strings.TrimPrefix(p, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func (d *driver) walkDir(ctx context.Context, c ftpConn, dir string, fn func(storagecore.Entry) error) error {
	entries, err := c.List(dir)
	if err != nil {
		return err
	}
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
		if err := fn(entry); err != nil {
			return err
		}
		if entry.IsDir {
			if err := d.walkDir(ctx, c, full, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func wrapError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not found") || strings.Contains(msg, "not available") || strings.Contains(msg, "no such file") || strings.Contains(msg, "can't check for file existence") || strings.Contains(msg, "550") {
		return fmt.Errorf("%w: %v", storagecore.ErrNotFound, err)
	}
	return err
}

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

func (c realFTPConn) Login(user, password string) error {
	return c.conn.Login(user, password)
}

func (c realFTPConn) Quit() error {
	return c.conn.Quit()
}

func (c realFTPConn) Retr(path string) (io.ReadCloser, error) {
	return c.conn.Retr(path)
}

func (c realFTPConn) Stor(path string, reader io.Reader) error {
	return c.conn.Stor(path, reader)
}

func (c realFTPConn) Delete(path string) error {
	return c.conn.Delete(path)
}

func (c realFTPConn) List(path string) ([]*ftp.Entry, error) {
	return c.conn.List(path)
}

func (c realFTPConn) FileSize(path string) (int64, error) {
	return c.conn.FileSize(path)
}

func (c realFTPConn) MakeDir(path string) error {
	return c.conn.MakeDir(path)
}
