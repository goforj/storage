package storagetest

import (
	"testing"

	"github.com/goforj/storage"
	memorystorage "github.com/goforj/storage/driver/memorystorage"
)

// Fake returns an in-memory storage backend for tests.
func Fake(t testing.TB) storage.Storage {
	t.Helper()
	return FakeWithPrefix(t, "")
}

// FakeWithPrefix returns an in-memory storage backend for tests with a fixed prefix.
func FakeWithPrefix(t testing.TB, prefix string) storage.Storage {
	t.Helper()
	store, err := storage.Build(memorystorage.Config{Prefix: prefix})
	if err != nil {
		t.Fatalf("storagetest.FakeWithPrefix: %v", err)
	}
	return store
}

// FakeManager returns a manager backed by in-memory disks for tests.
//
// If disks is empty, a single default disk is created.
func FakeManager(t testing.TB, defaultDisk storage.DiskName, disks map[storage.DiskName]memorystorage.Config) *storage.Manager {
	t.Helper()

	if defaultDisk == "" {
		defaultDisk = "default"
	}
	if len(disks) == 0 {
		disks = map[storage.DiskName]memorystorage.Config{
			defaultDisk: {},
		}
	}

	driverConfigs := make(map[storage.DiskName]storage.DriverConfig, len(disks))
	for name, cfg := range disks {
		driverConfigs[name] = cfg
	}

	mgr, err := storage.New(storage.Config{
		Default: defaultDisk,
		Disks:   driverConfigs,
	})
	if err != nil {
		t.Fatalf("storagetest.FakeManager: %v", err)
	}
	return mgr
}
