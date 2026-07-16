package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Manager holds named storage disks.
// @group Manager
//
// Example: keep a manager for later disk lookups
//
//	mgr, _ := storage.New(storage.Config{
//		Default: "local",
//		Disks: map[storage.DiskName]storage.DriverConfig{
//			"local": localstorage.Config{Root: "/tmp/storage-manager"},
//		},
//	})
//	_ = mgr
type Manager struct {
	defaultDisk DiskName
	disks       map[DiskName]Storage
	order       []DiskName
	closeOnce   sync.Once
	closeErr    error
}

// New constructs a Manager and eagerly initializes all disks.
// @group Manager
//
// Example: build a manager with named disks
//
//	mgr, _ := storage.New(storage.Config{
//		Default: "local",
//		Disks: map[storage.DiskName]storage.DriverConfig{
//			"local":  localstorage.Config{Root: "/tmp/storage-local"},
//			"assets": localstorage.Config{Root: "/tmp/storage-assets", Prefix: "public"},
//		},
//	})
//	_ = mgr
func New(cfg Config) (*Manager, error) {
	if cfg.Default == "" {
		return nil, fmt.Errorf("storage: default disk is required")
	}
	if len(cfg.Disks) == 0 {
		return nil, fmt.Errorf("storage: at least one disk is required")
	}
	if _, ok := cfg.Disks[cfg.Default]; !ok {
		return nil, fmt.Errorf("storage: default disk %q is not configured", cfg.Default)
	}

	type diskPlan struct {
		name    DiskName
		factory DriverFactory
		config  ResolvedConfig
	}
	names := make([]DiskName, 0, len(cfg.Disks))
	for name := range cfg.Disks {
		names = append(names, name)
	}
	slices.Sort(names)

	plans := make([]diskPlan, 0, len(names))
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("storage: disk name is required")
		}
		driverName, diskCfg, err := resolveDriverConfig(cfg.Disks[name])
		if err != nil {
			return nil, fmt.Errorf("storage: initialize disk %q: %w", name, err)
		}
		factory, ok := lookupDriver(driverName)
		if !ok {
			return nil, fmt.Errorf("storage: unknown driver %q for disk %q", driverName, name)
		}
		plans = append(plans, diskPlan{name: name, factory: factory, config: diskCfg})
	}

	disks := make(map[DiskName]Storage, len(cfg.Disks))
	for _, plan := range plans {
		d, err := plan.factory(context.Background(), plan.config)
		if err != nil {
			initErr := fmt.Errorf("storage: initialize disk %q: %w", plan.name, err)
			return nil, joinManagerCleanup(initErr, closeDisks(disks, namesBefore(names, plan.name)))
		}
		if isNil(d) {
			initErr := fmt.Errorf("storage: initialize disk %q: driver returned a nil store", plan.name)
			return nil, joinManagerCleanup(initErr, closeDisks(disks, namesBefore(names, plan.name)))
		}
		disks[plan.name] = d
	}

	return &Manager{
		defaultDisk: cfg.Default,
		disks:       disks,
		order:       slices.Clone(names),
	}, nil
}

// Close releases all initialized disks once in reverse deterministic construction order.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.closeErr = closeDisks(m.disks, m.order)
	})
	return m.closeErr
}

// closeDisks closes optional driver resources in reverse order and retains every failure.
func closeDisks(disks map[DiskName]Storage, order []DiskName) error {
	var closeErrs []error
	for i := len(order) - 1; i >= 0; i-- {
		disk, ok := disks[order[i]]
		if !ok {
			continue
		}
		closer, ok := disk.(interface{ Close() error })
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("storage: close disk %q: %w", order[i], err))
		}
	}
	return errors.Join(closeErrs...)
}

// namesBefore returns the deterministic prefix initialized before name.
func namesBefore(names []DiskName, name DiskName) []DiskName {
	index, _ := slices.BinarySearch(names, name)
	return names[:index]
}

// joinManagerCleanup preserves the initialization error exactly when cleanup succeeds.
func joinManagerCleanup(initErr, cleanupErr error) error {
	if cleanupErr == nil {
		return initErr
	}
	return errors.Join(initErr, cleanupErr)
}

// Default returns the default disk or panics if misconfigured.
// @group Manager
//
// Example: get the default disk
//
//	mgr, _ := storage.New(storage.Config{
//		Default: "local",
//		Disks: map[storage.DiskName]storage.DriverConfig{
//			"local": localstorage.Config{Root: "/tmp/storage-default"},
//		},
//	})
//
//	fs := mgr.Default()
//	fmt.Println(fs != nil)
//	// Output: true
func (m *Manager) Default() Storage {
	d, ok := m.disks[m.defaultDisk]
	if !ok {
		panic("storage: default disk misconfigured")
	}
	return d
}

// Disk returns a named disk or an error if it does not exist.
// @group Manager
//
// Example: get a named disk
//
//	mgr, _ := storage.New(storage.Config{
//		Default: "local",
//		Disks: map[storage.DiskName]storage.DriverConfig{
//			"local":   localstorage.Config{Root: "/tmp/storage-default"},
//			"uploads": localstorage.Config{Root: "/tmp/storage-uploads"},
//		},
//	})
//
//	fs, _ := mgr.Disk("uploads")
//	fmt.Println(fs != nil)
//	// Output: true
func (m *Manager) Disk(name DiskName) (Storage, error) {
	d, ok := m.disks[name]
	if !ok {
		return nil, fmt.Errorf("storage: disk %q not found", name)
	}
	return d, nil
}
