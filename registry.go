package storage

import "context"

import storagecore "github.com/goforj/storage/storagecore"

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
	storagecore.RegisterDriver(name, func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		store, err := factory(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if wrapped, ok := store.(*boundStorage); ok {
			return wrapped.inner, nil
		}
		if coreStore, ok := store.(storagecore.Storage); ok {
			return coreStore, nil
		}
		return nil, ErrUnsupported
	})
}

func lookupDriver(name string) (DriverFactory, bool) {
	factory, ok := storagecore.LookupDriver(name)
	if !ok {
		return nil, false
	}
	return func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		store, err := factory(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return wrapStorage(store), nil
	}, true
}
