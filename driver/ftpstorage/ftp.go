package ftpstorage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"path"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/goforj/storage"
)

func init() {
	storage.RegisterDriver("ftp", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	addr     string
	user     string
	pass     string
	prefix   string
	tls      bool
	insecure bool
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

func (c Config) ResolvedConfig() storage.ResolvedConfig {
	return storage.ResolvedConfig{
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
func New(cfg Config) (storage.Storage, error) {
	return NewContext(context.Background(), cfg)
}

func NewContext(ctx context.Context, cfg Config) (storage.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

func newFromDiskConfig(_ context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
	if cfg.FTPHost == "" {
		return nil, fmt.Errorf("storage: ftp requires FTPHost")
	}
	user := cfg.FTPUser
	pass := cfg.FTPPassword
	port := cfg.FTPPort
	if port == 0 {
		port = 21
	}
	prefix, err := storage.NormalizePath(cfg.Prefix)
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
	}, nil
}

func (d *driver) dial() (*ftp.ServerConn, error) {
	opts := []ftp.DialOption{
		ftp.DialWithTimeout(10 * time.Second),
		ftp.DialWithDisabledEPSV(true),
	}
	if d.tls {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{InsecureSkipVerify: d.insecure}))
	}
	return ftp.Dial(d.addr, opts...)
}

func (d *driver) withConn(fn func(*ftp.ServerConn) error) error {
	conn, err := d.dial()
	if err != nil {
		return err
	}
	defer conn.Quit()
	if d.user != "" || d.pass != "" {
		if err := conn.Login(d.user, d.pass); err != nil {
			return err
		}
	}
	return fn(conn)
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
	err = d.withConn(func(c *ftp.ServerConn) error {
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
	return wrapError(d.withConn(func(c *ftp.ServerConn) error {
		dir := path.Dir(fp)
		if dir != "" && dir != "." {
			_ = ensureDirs(c, dir)
		}
		return c.Stor(fp, bytes.NewReader(contents))
	}))
}

func ensureDirs(c *ftp.ServerConn, dir string) error {
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
	return wrapError(d.withConn(func(c *ftp.ServerConn) error {
		return c.Delete(fp)
	}))
}

func (d *driver) Stat(p string) (storage.Entry, error) {
	return d.StatContext(context.Background(), p)
}

func (d *driver) StatContext(ctx context.Context, p string) (storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storage.Entry{}, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return storage.Entry{}, err
	}
	var entry storage.Entry
	err = d.withConn(func(c *ftp.ServerConn) error {
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
			entry = storage.Entry{Path: d.stripPrefix(fp), Size: size, IsDir: isDir}
			return nil
		}
		return &textproto.Error{Code: 550, Msg: "not found"}
	})
	if err != nil {
		return storage.Entry{}, wrapError(err)
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
	err = d.withConn(func(c *ftp.ServerConn) error {
		_, err := c.FileSize(fp)
		return err
	})
	if err != nil {
		wrapped := wrapError(err)
		if errors.Is(wrapped, storage.ErrNotFound) {
			return false, nil
		}
		return false, wrapped
	}
	return true, nil
}

func (d *driver) List(p string) ([]storage.Entry, error) {
	return d.ListContext(context.Background(), p)
}

func (d *driver) ListContext(ctx context.Context, p string) ([]storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	var entries []storage.Entry
	err = d.withConn(func(c *ftp.ServerConn) error {
		l, err := c.List(fp)
		if err != nil {
			return err
		}
		for _, e := range l {
			rel := e.Name
			if fp != "" && fp != "." && fp != "/" {
				rel = path.Join(d.stripPrefix(fp), e.Name)
			}
			entries = append(entries, storage.Entry{
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

func (d *driver) Walk(p string, fn func(storage.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

func (d *driver) WalkContext(ctx context.Context, p string, fn func(storage.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return err
	}
	return wrapError(d.withConn(func(c *ftp.ServerConn) error {
		if err := d.walkDir(ctx, c, fp, fn); err == nil {
			return nil
		} else if wrapped := wrapError(err); !errors.Is(wrapped, storage.ErrNotFound) {
			return err
		}

		size, err := c.FileSize(fp)
		if err != nil {
			return err
		}
		return fn(storage.Entry{Path: d.stripPrefix(fp), Size: size, IsDir: false})
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
	return "", fmt.Errorf("%w: public URL not supported for ftp", storage.ErrUnsupported)
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

func (d *driver) walkDir(ctx context.Context, c *ftp.ServerConn, dir string, fn func(storage.Entry) error) error {
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
		entry := storage.Entry{
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
		return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
	}
	return err
}
