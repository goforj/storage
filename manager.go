package storage

import (
	"context"
	"fmt"
)

// Manager holds the disk registry.
type Manager struct {
	defaultDisk DiskName
	disks       map[DiskName]Storage
}

// New constructs a Manager and eagerly initializes all disks.
// @group Manager
func New(cfg Config) (*Manager, error) {
	if cfg.Default == "" {
		return nil, fmt.Errorf("storage: default disk is required")
	}
	if len(cfg.Disks) == 0 {
		return nil, fmt.Errorf("storage: at least one disk is required")
	}

	disks := make(map[DiskName]Storage, len(cfg.Disks))
	for name, driverCfg := range cfg.Disks {
		driverName, diskCfg, err := resolveDriverConfig(driverCfg)
		if err != nil {
			return nil, fmt.Errorf("storage: initialize disk %q: %w", name, err)
		}
		factory, ok := lookupDriver(driverName)
		if !ok {
			return nil, fmt.Errorf("storage: unknown driver %q for disk %q", driverName, name)
		}
		d, err := factory(context.Background(), diskCfg)
		if err != nil {
			return nil, fmt.Errorf("storage: initialize disk %q: %w", name, err)
		}
		disks[name] = d
	}

	return &Manager{
		defaultDisk: cfg.Default,
		disks:       disks,
	}, nil
}

// Default returns the default disk or panics if misconfigured.
// @group Manager
func (m *Manager) Default() Storage {
	d, ok := m.disks[m.defaultDisk]
	if !ok {
		panic("storage: default disk misconfigured")
	}
	return d
}

// Disk returns a named disk or an error if it does not exist.
// @group Manager
func (m *Manager) Disk(name DiskName) (Storage, error) {
	d, ok := m.disks[name]
	if !ok {
		return nil, fmt.Errorf("storage: disk %q not found", name)
	}
	return d, nil
}
