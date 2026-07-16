package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type fakeDriverConfig struct {
	name     string
	resolved ResolvedConfig
}

// DriverName exposes the registry key selected by each construction test.
func (c fakeDriverConfig) DriverName() string { return c.name }

// ResolvedConfig returns the payload used to exercise config normalization.
func (c fakeDriverConfig) ResolvedConfig() ResolvedConfig {
	return c.resolved
}

// TestBuild verifies registry construction fills the resolved driver name and returns a usable handle.
func TestBuild(t *testing.T) {
	driverName := fmt.Sprintf("fake-build-%s", t.Name())
	RegisterDriver(driverName, func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cfg.Driver != driverName {
			t.Fatalf("unexpected resolved driver %q", cfg.Driver)
		}
		return stubFS{}, nil
	})

	cfg := fakeDriverConfig{name: driverName}
	got, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got == nil {
		t.Fatal("Build returned nil storage")
	}
}

// TestBuildContext propagates a canceled construction context into the registered factory.
func TestBuildContext(t *testing.T) {
	driverName := fmt.Sprintf("fake-build-context-%s", t.Name())
	RegisterDriver(driverName, func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		return nil, ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := BuildContext(ctx, fakeDriverConfig{name: driverName})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildContext error = %v, want context.Canceled", err)
	}
}

// TestBuildErrors covers nil configs, mismatched names, and missing registry entries.
func TestBuildErrors(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		_, err := Build(nil)
		if err == nil || err.Error() != "storage: driver config is required" {
			t.Fatalf("Build(nil) error = %v", err)
		}
	})

	t.Run("mismatched driver", func(t *testing.T) {
		_, err := Build(fakeDriverConfig{
			name:     "left",
			resolved: ResolvedConfig{Driver: "right"},
		})
		if err == nil || err.Error() != `storage: driver config mismatch: "right" != "left"` {
			t.Fatalf("Build mismatch error = %v", err)
		}
	})

	t.Run("unknown driver", func(t *testing.T) {
		_, err := Build(fakeDriverConfig{name: "does-not-exist"})
		if err == nil || err.Error() != `storage: unknown driver "does-not-exist"` {
			t.Fatalf("Build unknown driver error = %v", err)
		}
	})
}

// TestManagerNewAndDefault verifies named disks and the configured default are both retrievable.
func TestManagerNewAndDefault(t *testing.T) {
	driverName := fmt.Sprintf("fake-manager-%s", t.Name())
	RegisterDriver(driverName, func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		return stubFS{}, nil
	})

	mgr, err := New(Config{
		Default: "default",
		Disks: map[DiskName]DriverConfig{
			"default": fakeDriverConfig{name: driverName},
			"other":   fakeDriverConfig{name: driverName},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if mgr.Default() == nil {
		t.Fatal("Default returned nil storage")
	}

	disk, err := mgr.Disk("other")
	if err != nil {
		t.Fatalf("Disk: %v", err)
	}
	if disk == nil {
		t.Fatal("Disk returned nil storage")
	}
}

// TestManagerErrors covers invalid configuration, missing lookups, and the Default panic contract.
func TestManagerErrors(t *testing.T) {
	t.Run("missing default", func(t *testing.T) {
		_, err := New(Config{Disks: map[DiskName]DriverConfig{"x": fakeDriverConfig{name: "fake"}}})
		if err == nil || err.Error() != "storage: default disk is required" {
			t.Fatalf("New missing default error = %v", err)
		}
	})

	t.Run("missing disks", func(t *testing.T) {
		_, err := New(Config{Default: "x"})
		if err == nil || err.Error() != "storage: at least one disk is required" {
			t.Fatalf("New missing disks error = %v", err)
		}
	})

	t.Run("missing disk lookup", func(t *testing.T) {
		mgr := &Manager{defaultDisk: "default", disks: map[DiskName]Storage{"default": stubFS{}}}
		_, err := mgr.Disk("missing")
		if err == nil || err.Error() != `storage: disk "missing" not found` {
			t.Fatalf("Disk missing error = %v", err)
		}
	})

	t.Run("default panic", func(t *testing.T) {
		mgr := &Manager{defaultDisk: "default", disks: map[DiskName]Storage{}}
		defer func() {
			if recover() == nil {
				t.Fatal("Default did not panic")
			}
		}()
		_ = mgr.Default()
	})
}

// TestResolveDriverConfig fills an omitted resolved name and rejects a missing typed name.
func TestResolveDriverConfig(t *testing.T) {
	t.Run("fills driver from config name", func(t *testing.T) {
		name, resolved, err := resolveDriverConfig(fakeDriverConfig{name: "fake"})
		if err != nil {
			t.Fatalf("resolveDriverConfig: %v", err)
		}
		if name != "fake" || resolved.Driver != "fake" {
			t.Fatalf("got name=%q resolved.Driver=%q", name, resolved.Driver)
		}
	})

	t.Run("missing name", func(t *testing.T) {
		_, _, err := resolveDriverConfig(fakeDriverConfig{})
		if err == nil || err.Error() != "storage: driver name is required" {
			t.Fatalf("resolveDriverConfig missing name error = %v", err)
		}
	})
}
