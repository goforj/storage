package storage

import (
	"context"
	"fmt"
	"sync"
)

// DriverFactory constructs a Storage for a given normalized disk configuration.
// @group Construction
//
// Example: declare a driver factory
//
//	factory := storage.DriverFactory(func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
//		return nil, nil
//	})
//	_ = factory
type DriverFactory func(ctx context.Context, cfg ResolvedConfig) (Storage, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]DriverFactory{}
)

// RegisterDriver makes a driver available to the Manager. It panics on duplicate registrations.
// @group Manager
//
// Example: register a custom driver
//
//	storage.RegisterDriver("memory", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
//		return nil, nil
//	})
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
