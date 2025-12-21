package filesystem

import (
	"context"
	"errors"
	"testing"
)

type stubFS struct{}

func (stubFS) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (stubFS) Put(context.Context, string, []byte) error   { return nil }
func (stubFS) Delete(context.Context, string) error        { return nil }
func (stubFS) Exists(context.Context, string) (bool, error) { return true, nil }
func (stubFS) List(context.Context, string) ([]Entry, error) { return nil, nil }
func (stubFS) URL(context.Context, string) (string, error)   { return "", nil }

func TestManagerNewErrors(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("expected error for missing default")
	}
	if _, err := New(Config{Default: "disk"}); err == nil {
		t.Fatalf("expected error for missing disks")
	}

	// unknown driver
	_, err := New(Config{
		Default: "missing",
		Disks: map[DiskName]DiskConfig{
			"missing": {Driver: "nope"},
		},
	})
	if err == nil {
		t.Fatalf("expected unknown driver error")
	}

	// driver factory returns error
	RegisterDriver("stub-error", func(context.Context, DiskConfig, Config) (Filesystem, error) {
		return nil, errors.New("boom")
	})
	_, err = New(Config{
		Default: "bad",
		Disks: map[DiskName]DiskConfig{
			"bad": {Driver: "stub-error"},
		},
	})
	if err == nil {
		t.Fatalf("expected factory error")
	}
}

func TestManagerSuccessAndLookups(t *testing.T) {
	RegisterDriver("stub-ok", func(context.Context, DiskConfig, Config) (Filesystem, error) {
		return stubFS{}, nil
	})
	cfg := Config{
		Default: "primary",
		Disks: map[DiskName]DiskConfig{
			"primary": {Driver: "stub-ok"},
			"other":   {Driver: "stub-ok"},
		},
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := m.Disk("primary"); err != nil {
		t.Fatalf("Disk existing: %v", err)
	}
	if _, err := m.Disk("missing"); err == nil {
		t.Fatalf("expected Disk missing error")
	}
	// default points to non-existent disk should panic when accessed
	m.defaultDisk = "missing"
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic when default disk missing")
		}
	}()
	_ = m.Default()
}
