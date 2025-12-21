package ftpdriver

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/goforj/filesystem"
)

func init() {
	filesystem.RegisterDriver("ftp", New)
}

type Driver struct {
	addr     string
	user     string
	pass     string
	prefix   string
	tls      bool
	insecure bool
}

// New constructs an FTP-backed filesystem using jlaffaye/ftp.
// @group Drivers
//
// Example: ftp driver
//
//	fs, _ := ftpdriver.New(context.Background(), filesystem.DiskConfig{Driver: "ftp", FTPHost: "127.0.0.1", FTPUser: "anonymous"}, filesystem.Config{})
func New(_ context.Context, cfg filesystem.DiskConfig, _ filesystem.Config) (filesystem.Filesystem, error) {
	if cfg.FTPHost == "" {
		return nil, fmt.Errorf("filesystem: ftp requires FTPHost")
	}
	user := cfg.FTPUser
	pass := cfg.FTPPassword
	port := cfg.FTPPort
	if port == 0 {
		port = 21
	}
	prefix, err := filesystem.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("%s:%d", cfg.FTPHost, port)

	return &Driver{
		addr:     addr,
		user:     user,
		pass:     pass,
		prefix:   prefix,
		tls:      cfg.FTPTLS,
		insecure: cfg.FTPInsecureSkipVerify,
	}, nil
}

func (d *Driver) dial() (*ftp.ServerConn, error) {
	opts := []ftp.DialOption{
		ftp.DialWithTimeout(10 * time.Second),
		ftp.DialWithDisabledEPSV(true),
	}
	if d.tls {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{InsecureSkipVerify: d.insecure}))
	}
	return ftp.Dial(d.addr, opts...)
}

func (d *Driver) withConn(fn func(*ftp.ServerConn) error) error {
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

func (d *Driver) Get(ctx context.Context, p string) ([]byte, error) {
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

func (d *Driver) Put(ctx context.Context, p string, contents []byte) error {
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

func (d *Driver) Delete(ctx context.Context, p string) error {
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

func (d *Driver) Exists(ctx context.Context, p string) (bool, error) {
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
		if errors.Is(wrapped, filesystem.ErrNotFound) {
			return false, nil
		}
		return false, wrapped
	}
	return true, nil
}

func (d *Driver) List(ctx context.Context, p string) ([]filesystem.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fp, err := d.fullPath(p)
	if err != nil {
		return nil, err
	}
	var entries []filesystem.Entry
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
			entries = append(entries, filesystem.Entry{
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

func (d *Driver) URL(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: public URL not supported for ftp", filesystem.ErrUnsupported)
}

func (d *Driver) fullPath(p string) (string, error) {
	normalized, err := filesystem.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return filesystem.JoinPrefix(d.prefix, normalized), nil
}

func (d *Driver) stripPrefix(p string) string {
	if d.prefix == "" {
		return p
	}
	trimmed := strings.TrimPrefix(p, d.prefix)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return trimmed
}

func wrapError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not found") || strings.Contains(msg, "not available") || strings.Contains(msg, "no such file") || strings.Contains(msg, "can't check for file existence") || strings.Contains(msg, "550") {
		return fmt.Errorf("%w: %v", filesystem.ErrNotFound, err)
	}
	return err
}
