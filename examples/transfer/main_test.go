package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/memorystorage"
)

// TestSelectDrivers checks that only explicit selection enables live Dropbox.
func TestSelectDrivers(t *testing.T) {
	for _, tt := range []struct {
		selection string
		token     bool
		want      []string
		message   string
	}{
		{selection: "all", token: true, want: fixtureDrivers},
		{selection: "local, memory", want: []string{"local", "memory"}},
		{selection: "dropbox", token: true, want: []string{"dropbox"}},
		{selection: "dropbox", message: "requires STORAGE_TRANSFER_DROPBOX_TOKEN"},
		{selection: "unknown", message: "unknown driver"},
	} {
		got, err := selectDrivers(tt.selection, tt.token)
		if tt.message != "" {
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Errorf("select %q error = %v, want %q", tt.selection, err, tt.message)
			}
		} else if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("select %q = %v, %v, want %v", tt.selection, got, err, tt.want)
		}
	}
}

// TestRunRejectsInvalidSizes keeps the object allocation bounded.
func TestRunRejectsInvalidSizes(t *testing.T) {
	for _, bounds := range [][2]int64{{0, 32}, {1, 0}, {1, 129}} {
		if err := run(context.Background(), "memory", bounds[0], bounds[1], "", false); err == nil {
			t.Errorf("run accepted bytes=%d object-mib=%d", bounds[0], bounds[1])
		}
	}
}

type testDisk struct {
	storage.Storage
	corrupt   bool
	uploadErr error
	deleteErr error
}

// WithContext retains the injected errors when transfer binds another context.
func (d testDisk) WithContext(ctx context.Context) storage.Storage {
	d.Storage = d.Storage.WithContext(ctx)
	return d
}

// Get can simulate a same-length corrupted download.
func (d testDisk) Get(key string) ([]byte, error) {
	data, err := d.Storage.Get(key)
	if err == nil && d.corrupt {
		data[0] ^= 1
	}
	return data, err
}

// Put can simulate a server committing the object before reporting failure.
func (d testDisk) Put(key string, data []byte) error {
	return errors.Join(d.Storage.Put(key, data), d.uploadErr)
}

// Delete can simulate a cleanup failure after a successful delete.
func (d testDisk) Delete(key string) error {
	return errors.Join(d.Storage.Delete(key), d.deleteErr)
}

// TestTransferVerifiesAndCleansUp covers verification and ambiguous-write cleanup.
func TestTransferVerifiesAndCleansUp(t *testing.T) {
	uploadErr := errors.New("upload acknowledgement lost")
	deleteErr := errors.New("delete acknowledgement lost")
	for _, tt := range []struct {
		name    string
		disk    testDisk
		want    error
		message string
	}{
		{name: "verified"},
		{name: "corrupt read", disk: testDisk{corrupt: true}, message: "content mismatch"},
		{name: "ambiguous write", disk: testDisk{uploadErr: uploadErr}, want: uploadErr},
		{name: "cleanup error", disk: testDisk{deleteErr: deleteErr}, want: deleteErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			disk, err := storage.Build(memorystorage.Config{})
			if err != nil {
				t.Fatal(err)
			}
			tt.disk.Storage = disk
			_, _, err = transfer(context.Background(), tt.disk, nil, "fixture", []byte("payload"))
			if tt.message != "" {
				if err == nil || !strings.Contains(err.Error(), tt.message) {
					t.Fatalf("transfer error = %v, want %q", err, tt.message)
				}
			} else if !errors.Is(err, tt.want) {
				t.Fatalf("transfer error = %v, want %v", err, tt.want)
			}
			if _, err := disk.Get("fixture"); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("cleanup left object behind: %v", err)
			}
		})
	}
}
