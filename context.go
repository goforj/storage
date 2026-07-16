package storage

import "context"

// normalizeContext keeps context methods nil-safe without expanding the cross-module storagecore API.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
