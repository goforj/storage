package storage

import (
	"context"
	"errors"
	"slices"
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

type closeTrackingFS struct {
	stubFS
	name  string
	calls *[]string
	err   error
}

// Close records deterministic manager shutdown order and returns its configured failure.
func (s *closeTrackingFS) Close() error {
	*s.calls = append(*s.calls, s.name)
	return s.err
}

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
	errorDriverName := uniqueTestDriverName("stub-error")
	RegisterDriver(errorDriverName, func(context.Context, ResolvedConfig) (Storage, error) {
		return nil, errors.New("boom")
	})
	_, err = New(Config{
		Default: "bad",
		Disks: map[DiskName]DriverConfig{
			"bad": stubDriverConfig{name: errorDriverName},
		},
	})
	if err == nil {
		t.Fatalf("expected factory error")
	}
}

// TestManagerConfigurationValidation verifies named-disk errors are classified before construction.
func TestManagerConfigurationValidation(t *testing.T) {
	tests := map[string]Config{
		"default not configured": {
			Default: "missing",
			Disks:   map[DiskName]DriverConfig{"other": stubDriverConfig{name: "unused"}},
		},
		"empty disk name": {
			Default: "valid",
			Disks: map[DiskName]DriverConfig{
				"":      stubDriverConfig{name: "unused"},
				"valid": stubDriverConfig{name: "unused"},
			},
		},
		"nil disk config": {
			Default: "disk",
			Disks:   map[DiskName]DriverConfig{"disk": nil},
		},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("New returned nil error")
			}
		})
	}
}

// TestManagerSuccessAndLookups verifies named lookup and the missing-default panic contract.
func TestManagerSuccessAndLookups(t *testing.T) {
	driverName := uniqueTestDriverName("stub-ok")
	RegisterDriver(driverName, func(context.Context, ResolvedConfig) (Storage, error) {
		return stubFS{}, nil
	})
	cfg := Config{
		Default: "primary",
		Disks: map[DiskName]DriverConfig{
			"primary": stubDriverConfig{name: driverName},
			"other":   stubDriverConfig{name: driverName},
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

// TestManagerClose verifies reverse-order cleanup, joined failures, and idempotence.
func TestManagerClose(t *testing.T) {
	if err := (*Manager)(nil).Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}

	var calls []string
	firstErr := errors.New("first close")
	lastErr := errors.New("last close")
	manager := &Manager{
		disks: map[DiskName]Storage{
			"first":  &closeTrackingFS{name: "first", calls: &calls, err: firstErr},
			"middle": stubFS{},
			"last":   &closeTrackingFS{name: "last", calls: &calls, err: lastErr},
		},
		order: []DiskName{"first", "missing", "middle", "last"},
	}

	err := manager.Close()
	if !errors.Is(err, firstErr) || !errors.Is(err, lastErr) {
		t.Fatalf("Close error = %v", err)
	}
	if want := []string{"last", "first"}; !slices.Equal(calls, want) {
		t.Fatalf("Close calls = %v want %v", calls, want)
	}
	if secondErr := manager.Close(); secondErr != err {
		t.Fatalf("second Close error = %v want original %v", secondErr, err)
	}
	if want := []string{"last", "first"}; !slices.Equal(calls, want) {
		t.Fatalf("second Close calls = %v want %v", calls, want)
	}
}

// TestManagerNewCleansUpInitializedDisks verifies failed construction closes earlier disks.
func TestManagerNewCleansUpInitializedDisks(t *testing.T) {
	var calls []string
	closeErr := errors.New("cleanup close")
	initErr := errors.New("construction")
	name := uniqueTestDriverName("manager-cleanup")
	RegisterDriver(name, func(_ context.Context, cfg ResolvedConfig) (Storage, error) {
		if cfg.Remote == "fail" {
			return nil, initErr
		}
		return &closeTrackingFS{name: cfg.Remote, calls: &calls, err: closeErr}, nil
	})

	_, err := New(Config{
		Default: "a",
		Disks: map[DiskName]DriverConfig{
			"a": stubDriverConfig{name: name, cfg: ResolvedConfig{Remote: "a"}},
			"b": stubDriverConfig{name: name, cfg: ResolvedConfig{Remote: "b"}},
			"c": stubDriverConfig{name: name, cfg: ResolvedConfig{Remote: "fail"}},
		},
	})
	if !errors.Is(err, initErr) || !errors.Is(err, closeErr) {
		t.Fatalf("New error = %v", err)
	}
	if want := []string{"b", "a"}; !slices.Equal(calls, want) {
		t.Fatalf("cleanup calls = %v want %v", calls, want)
	}
}
