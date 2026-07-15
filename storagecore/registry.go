package storagecore

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// DriverFactory constructs a core storage implementation from resolved configuration.
type DriverFactory func(ctx context.Context, cfg ResolvedConfig) (Storage, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]DriverFactory{}
)

// RegisterDriver installs a validated, unique factory for process-wide construction.
func RegisterDriver(name string, factory DriverFactory) {
	if name == "" || name != strings.TrimSpace(name) {
		panic("storage: driver name must be non-empty and have no surrounding whitespace")
	}
	if factory == nil {
		panic(fmt.Sprintf("storage: driver %q factory is nil", name))
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("storage: driver %q already registered", name))
	}
	registry[name] = factory
}

// LookupDriver returns the factory registered under name without holding the registry lock afterward.
func LookupDriver(name string) (DriverFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	factory, ok := registry[name]
	return factory, ok
}
