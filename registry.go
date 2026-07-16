package storage

import (
	"context"
	"fmt"

	"github.com/goforj/storage/storagecore"
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

// RegisterDriver makes a driver available to the Manager. It panics on duplicate registrations.
// @group Manager
//
// Example: register a custom driver
//
//	storage.RegisterDriver("memory", func(ctx context.Context, cfg storage.ResolvedConfig) (storage.Storage, error) {
//		return nil, nil
//	})
func RegisterDriver(name string, factory DriverFactory) {
	if factory == nil {
		panic(fmt.Sprintf("storage: driver %q factory is nil", name))
	}
	storagecore.RegisterDriver(name, func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		store, err := factory(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if isNil(store) {
			return nil, fmt.Errorf("storage: driver %q returned a nil store", name)
		}
		if coreStore, ok := store.(storagecore.Storage); ok {
			return coreStore, nil
		}
		return nil, ErrUnsupported
	})
}

// lookupDriver adapts a core factory to the public Storage interface and rejects typed nil results.
func lookupDriver(name string) (DriverFactory, bool) {
	factory, ok := storagecore.LookupDriver(name)
	if !ok {
		return nil, false
	}
	return func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		ctx = normalizeContext(ctx)
		store, err := factory(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if isNil(store) {
			return nil, fmt.Errorf("storage: driver %q returned a nil store", name)
		}
		return wrapStorage(store), nil
	}, true
}
