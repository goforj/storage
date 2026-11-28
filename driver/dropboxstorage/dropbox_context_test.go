package dropboxstorage

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
)

type dropboxTransportFunc func(*http.Request) (*http.Response, error)

// RoundTrip lets the SDK build real HTTP requests while the test controls I/O.
func (f dropboxTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestDropboxRequestCancellation verifies active SDK requests inherit the operation's context.
func TestDropboxRequestCancellation(t *testing.T) {
	for _, tt := range []struct {
		name string
		path string
		call func(*driver, context.Context) error
	}{
		{"download", "/2/files/download", func(d *driver, ctx context.Context) error {
			_, err := d.GetContext(ctx, "file.txt")
			return err
		}},
		{"upload", "/2/files/upload", func(d *driver, ctx context.Context) error {
			return d.PutContext(ctx, "file.txt", []byte("example"))
		}},
		{"list", "/2/files/list_folder", func(d *driver, ctx context.Context) error {
			_, err := d.ListContext(ctx, "")
			return err
		}},
		{"metadata", "/2/files/get_metadata", func(d *driver, ctx context.Context) error {
			_, err := d.StatContext(ctx, "file.txt")
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var observed bool
			d := &driver{client: files.NewContext(dropbox.Config{
				Client: &http.Client{Transport: dropboxTransportFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path != tt.path {
						t.Errorf("request path = %q, want %q", req.URL.Path, tt.path)
					}
					cancel()
					observed = errors.Is(req.Context().Err(), context.Canceled)
					if !observed {
						return nil, errors.New("HTTP request did not inherit cancellation")
					}
					return nil, req.Context().Err()
				})},
			})}
			if err := tt.call(d, ctx); !errors.Is(err, context.Canceled) || !observed {
				t.Fatalf("operation error = %v, HTTP context canceled = %t", err, observed)
			}
		})
	}
}
