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

// DriverName returns the registry key selected by the manager fixture.
func (c stubDriverConfig) DriverName() string { return c.name }

// ResolvedConfig returns the prebuilt payload supplied by the manager fixture.
func (c stubDriverConfig) ResolvedConfig() ResolvedConfig { return c.cfg }

type stubFS struct{}

// WithContext keeps the stateless stub usable through the public binding path.
func (stubFS) WithContext(context.Context) Storage { return stubFS{} }

// Get returns an empty payload because manager tests do not exercise backend I/O.
func (stubFS) Get(string) ([]byte, error) { return nil, nil }

// Put accepts writes so the stub satisfies the public Storage contract.
func (stubFS) Put(string, []byte) error { return nil }

// MakeDir accepts directory creation without retaining fixture state.
func (stubFS) MakeDir(string) error { return nil }

// Delete accepts removals without retaining fixture state.
func (stubFS) Delete(string) error { return nil }

// Stat returns zero metadata because construction tests only require an implementation.
func (stubFS) Stat(string) (Entry, error) { return Entry{}, nil }

// Exists returns true to provide a deterministic inert implementation.
func (stubFS) Exists(string) (bool, error) { return true, nil }

// List returns no children because the stub has no backing state.
func (stubFS) List(string) ([]Entry, error) { return nil, nil }

// Walk reports unsupported traversal so accidental fixture use is visible.
func (stubFS) Walk(string, func(Entry) error) error {
	return ErrUnsupported
}

// Copy accepts duplication without retaining fixture state.
func (stubFS) Copy(string, string) error { return nil }

// Move accepts relocation without retaining fixture state.
func (stubFS) Move(string, string) error { return nil }

// URL returns no address because the manager fixture has no public endpoint.
func (stubFS) URL(string) (string, error) { return "", nil }

// TestManagerNewErrors covers missing configuration, unknown drivers, and factory failures.
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

// TestManagerSuccessAndLookups verifies named lookup and the missing-default panic contract.
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
