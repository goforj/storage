package memorystorage

import "context"

// normalizeContext preserves nil-context support without requiring an unreleased storagecore API.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
