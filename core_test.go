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

func (c fakeDriverConfig) DriverName() string { return c.name }
func (c fakeDriverConfig) ResolvedConfig() ResolvedConfig {
	return c.resolved
}

type fakeStorage struct{}

func (fakeStorage) WithContext(context.Context) Storage                    { return fakeStorage{} }
func (fakeStorage) Get(string) ([]byte, error)                          { return nil, nil }
func (fakeStorage) Put(string, []byte) error                            { return nil }
func (fakeStorage) MakeDir(string) error                                { return nil }
func (fakeStorage) Delete(string) error                                 { return nil }
func (fakeStorage) Stat(string) (Entry, error)                          { return Entry{}, nil }
func (fakeStorage) Exists(string) (bool, error)                         { return false, nil }
func (fakeStorage) List(string) ([]Entry, error)                        { return nil, nil }
func (fakeStorage) Walk(string, func(Entry) error) error                { return nil }
func (fakeStorage) Copy(string, string) error                           { return nil }
func (fakeStorage) Move(string, string) error                           { return nil }
func (fakeStorage) URL(string) (string, error)                          { return "", nil }
func (fakeStorage) GetContext(context.Context, string) ([]byte, error)  { return nil, nil }
func (fakeStorage) PutContext(context.Context, string, []byte) error    { return nil }
func (fakeStorage) MakeDirContext(context.Context, string) error        { return nil }
func (fakeStorage) DeleteContext(context.Context, string) error         { return nil }
func (fakeStorage) StatContext(context.Context, string) (Entry, error)  { return Entry{}, nil }
func (fakeStorage) ExistsContext(context.Context, string) (bool, error) { return false, nil }
func (fakeStorage) ListContext(context.Context, string) ([]Entry, error) {
	return nil, nil
}
func (fakeStorage) WalkContext(context.Context, string, func(Entry) error) error { return nil }
func (fakeStorage) CopyContext(context.Context, string, string) error            { return nil }
func (fakeStorage) MoveContext(context.Context, string, string) error            { return nil }
func (fakeStorage) URLContext(context.Context, string) (string, error)           { return "", nil }

func TestBuild(t *testing.T) {
	driverName := fmt.Sprintf("fake-build-%s", t.Name())
	RegisterDriver(driverName, func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cfg.Driver != driverName {
			t.Fatalf("unexpected resolved driver %q", cfg.Driver)
		}
		return fakeStorage{}, nil
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

func TestManagerNewAndDefault(t *testing.T) {
	driverName := fmt.Sprintf("fake-manager-%s", t.Name())
	RegisterDriver(driverName, func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		return fakeStorage{}, nil
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
		mgr := &Manager{defaultDisk: "default", disks: map[DiskName]Storage{"default": fakeStorage{}}}
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
