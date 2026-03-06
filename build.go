package storage

import "context"

// Build constructs a single storage backend from a typed driver config without
// a Manager.
func Build(ctx context.Context, cfg DriverConfig) (Storage, error) {
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
