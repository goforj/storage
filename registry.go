package storage

import (
	"context"
	"fmt"
	"sync"
)

// DriverFactory constructs a Storage for a given normalized disk configuration.
type DriverFactory func(ctx context.Context, cfg ResolvedConfig) (Storage, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]DriverFactory{}
)

// RegisterDriver makes a driver available to the Manager. It panics on duplicate registrations.
// @group Manager
func RegisterDriver(name string, factory DriverFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("storage: driver %q already registered", name))
	}
	registry[name] = factory
}

func lookupDriver(name string) (DriverFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	factory, ok := registry[name]
	return factory, ok
}
