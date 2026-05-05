package storage

import (
	"context"
	"errors"
	"testing"
)

type stubDriverConfig struct {
	name string
	cfg  ResolvedConfig
}

func (c stubDriverConfig) DriverName() string             { return c.name }
func (c stubDriverConfig) ResolvedConfig() ResolvedConfig { return c.cfg }

type stubFS struct{}

func (stubFS) WithContext(context.Context) Storage { return stubFS{} }
func (stubFS) Get(string) ([]byte, error)   { return nil, nil }
func (stubFS) Put(string, []byte) error     { return nil }
func (stubFS) MakeDir(string) error         { return nil }
func (stubFS) Delete(string) error          { return nil }
func (stubFS) Stat(string) (Entry, error)   { return Entry{}, nil }
func (stubFS) Exists(string) (bool, error)  { return true, nil }
func (stubFS) List(string) ([]Entry, error) { return nil, nil }
func (stubFS) Walk(string, func(Entry) error) error {
	return ErrUnsupported
}
func (stubFS) Copy(string, string) error  { return nil }
func (stubFS) Move(string, string) error  { return nil }
func (stubFS) URL(string) (string, error) { return "", nil }

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
		Disks: map[DiskName]DriverConfig{
			"missing": stubDriverConfig{name: "nope"},
		},
	})
	if err == nil {
		t.Fatalf("expected unknown driver error")
	}

	// driver factory returns error
	RegisterDriver("stub-error", func(context.Context, ResolvedConfig) (Storage, error) {
		return nil, errors.New("boom")
	})
	_, err = New(Config{
		Default: "bad",
		Disks: map[DiskName]DriverConfig{
			"bad": stubDriverConfig{name: "stub-error"},
		},
	})
	if err == nil {
		t.Fatalf("expected factory error")
	}
}

func TestManagerSuccessAndLookups(t *testing.T) {
	RegisterDriver("stub-ok", func(context.Context, ResolvedConfig) (Storage, error) {
		return stubFS{}, nil
	})
	cfg := Config{
		Default: "primary",
		Disks: map[DiskName]DriverConfig{
			"primary": stubDriverConfig{name: "stub-ok"},
			"other":   stubDriverConfig{name: "stub-ok"},
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
