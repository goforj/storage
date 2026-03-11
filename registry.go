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
		return factory(ctx, cfg)
	})
}

func lookupDriver(name string) (DriverFactory, bool) {
	factory, ok := storagecore.LookupDriver(name)
	if !ok {
		return nil, false
	}
	return func(ctx context.Context, cfg ResolvedConfig) (Storage, error) {
		return factory(ctx, cfg)
	}, true
}
