package storagecore

import (
	"context"
	"fmt"
	"sync"
)

type DriverFactory func(ctx context.Context, cfg ResolvedConfig) (Storage, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]DriverFactory{}
)

func RegisterDriver(name string, factory DriverFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("storage: driver %q already registered", name))
	}
	registry[name] = factory
}

func LookupDriver(name string) (DriverFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	factory, ok := registry[name]
	return factory, ok
}
