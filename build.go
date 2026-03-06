package storage

import "context"

// Build constructs a single storage backend from a typed driver config without
// a Manager.
// @group Construction
//
// Example: build a single disk
//
//	fs, _ := storage.Build(localstorage.Config{
//		Remote: "/tmp/storage-example",
//		Prefix: "assets",
//	})
//	_ = fs
func Build(cfg DriverConfig) (Storage, error) {
	return BuildContext(context.Background(), cfg)
}

// BuildContext constructs a single storage backend from a typed driver config
// using the caller-provided context.
// @group Context
func BuildContext(ctx context.Context, cfg DriverConfig) (Storage, error) {
	name, resolved, err := resolveDriverConfig(cfg)
	if err != nil {
		return nil, err
	}
	factory, ok := lookupDriver(name)
	if !ok {
		return nil, errUnknownDriver(name)
	}
	return factory(ctx, resolved)
}
