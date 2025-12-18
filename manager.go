package filesystem

import (
	"context"
	"fmt"
)

// Manager holds the disk registry.
type Manager struct {
	defaultDisk DiskName
	disks       map[DiskName]Filesystem
}

// New constructs a Manager and eagerly initializes all disks.
func New(cfg Config) (*Manager, error) {
	if cfg.Default == "" {
		return nil, fmt.Errorf("filesystem: default disk is required")
	}
	if len(cfg.Disks) == 0 {
		return nil, fmt.Errorf("filesystem: at least one disk is required")
	}

	disks := make(map[DiskName]Filesystem, len(cfg.Disks))
	for name, diskCfg := range cfg.Disks {
		factory, ok := lookupDriver(diskCfg.Driver)
		if !ok {
			return nil, fmt.Errorf("filesystem: unknown driver %q for disk %q", diskCfg.Driver, name)
		}

		d, err := factory(context.Background(), diskCfg, cfg)
		if err != nil {
			return nil, fmt.Errorf("filesystem: initialize disk %q: %w", name, err)
		}
		disks[name] = d
	}

	return &Manager{
		defaultDisk: cfg.Default,
		disks:       disks,
	}, nil
}

// Default returns the default disk or panics if misconfigured.
func (m *Manager) Default() Filesystem {
	d, ok := m.disks[m.defaultDisk]
	if !ok {
		panic("filesystem: default disk misconfigured")
	}
	return d
}

// Disk returns a named disk or an error if it does not exist.
func (m *Manager) Disk(name DiskName) (Filesystem, error) {
	d, ok := m.disks[name]
	if !ok {
		return nil, fmt.Errorf("filesystem: disk %q not found", name)
	}
	return d, nil
}
