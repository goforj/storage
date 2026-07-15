package storage

import (
	"context"
	"fmt"
	"sync"

	"github.com/goforj/storage/storagecore"
)

type boundStorage struct {
	inner      storagecore.Storage
	contextual storagecore.ContextStorage
	paged      storagecore.PagedStorage
	cpaged     storagecore.ContextPagedStorage
	binder     storageContextBinder
	ctx        context.Context
	lifecycle  *storageLifecycle
	bindErr    error
}

type storageContextBinder interface {
	WithContext(ctx context.Context) Storage
}

type storageLifecycle struct {
	once   sync.Once
	closer interface{ Close() error }
	err    error
}

// wrapStorage discovers optional driver capabilities and creates a shared close lifecycle.
func wrapStorage(inner storagecore.Storage) Storage {
	return wrapStorageWithLifecycle(inner, nil)
}

// wrapStorageWithLifecycle preserves one underlying closer across context-bound handles.
func wrapStorageWithLifecycle(inner storagecore.Storage, lifecycle *storageLifecycle) Storage {
	wrapped := &boundStorage{
		inner: inner,
	}
	if lifecycle != nil {
		wrapped.lifecycle = lifecycle
	} else if closer, ok := inner.(interface{ Close() error }); ok {
		wrapped.lifecycle = &storageLifecycle{closer: closer}
	}
	if binder, ok := inner.(storageContextBinder); ok {
		wrapped.binder = binder
	}
	if contextual, ok := inner.(storagecore.ContextStorage); ok {
		wrapped.contextual = contextual
	}
	if paged, ok := inner.(storagecore.PagedStorage); ok {
		wrapped.paged = paged
	}
	if cpaged, ok := inner.(storagecore.ContextPagedStorage); ok {
		wrapped.cpaged = cpaged
	}
	return wrapped
}

// Close releases the underlying driver's resources once across all context-bound clones.
func (s *boundStorage) Close() error {
	if s == nil || s.lifecycle == nil || s.lifecycle.closer == nil {
		return nil
	}
	s.lifecycle.once.Do(func() {
		s.lifecycle.err = s.lifecycle.closer.Close()
	})
	return s.lifecycle.err
}

// WithContext preserves a driver's public binding semantics before falling back to adapter dispatch.
func (s *boundStorage) WithContext(ctx context.Context) Storage {
	ctx = normalizeContext(ctx)
	if s != nil && s.binder != nil {
		bound := s.binder.WithContext(ctx)
		if isNil(bound) {
			clone := *s
			clone.ctx = ctx
			clone.binder = nil
			clone.bindErr = fmt.Errorf("storage: WithContext returned a nil storage")
			return &clone
		}
		coreBound, ok := bound.(storagecore.Storage)
		if !ok {
			clone := *s
			clone.ctx = ctx
			clone.binder = nil
			clone.bindErr = ErrUnsupported
			return &clone
		}
		return wrapStorageWithLifecycle(coreBound, s.lifecycle)
	}
	clone := *s
	clone.ctx = ctx
	return &clone
}

// context returns the bound context or a background context for the unbound handle.
func (s *boundStorage) context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Get uses the context-aware read capability when the driver exposes it.
func (s *boundStorage) Get(p string) ([]byte, error) {
	if s.bindErr != nil {
		return nil, s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.GetContext(s.context(), p)
	}
	return s.inner.Get(p)
}

// Put uses the context-aware write capability when the driver exposes it.
func (s *boundStorage) Put(p string, contents []byte) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.PutContext(s.context(), p, contents)
	}
	return s.inner.Put(p, contents)
}

// MakeDir uses the context-aware directory capability when the driver exposes it.
func (s *boundStorage) MakeDir(p string) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.MakeDirContext(s.context(), p)
	}
	return s.inner.MakeDir(p)
}

// Delete uses the context-aware removal capability when the driver exposes it.
func (s *boundStorage) Delete(p string) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.DeleteContext(s.context(), p)
	}
	return s.inner.Delete(p)
}

// Stat uses the context-aware metadata capability when the driver exposes it.
func (s *boundStorage) Stat(p string) (Entry, error) {
	if s.bindErr != nil {
		return Entry{}, s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.StatContext(s.context(), p)
	}
	return s.inner.Stat(p)
}

// Exists uses the context-aware existence capability when the driver exposes it.
func (s *boundStorage) Exists(p string) (bool, error) {
	if s.bindErr != nil {
		return false, s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.ExistsContext(s.context(), p)
	}
	return s.inner.Exists(p)
}

// List uses the context-aware one-level traversal capability when the driver exposes it.
func (s *boundStorage) List(p string) ([]Entry, error) {
	if s.bindErr != nil {
		return nil, s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.ListContext(s.context(), p)
	}
	return s.inner.List(p)
}

// Walk uses the context-aware recursive traversal capability when the driver exposes it.
func (s *boundStorage) Walk(p string, fn func(Entry) error) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.WalkContext(s.context(), p, fn)
	}
	return s.inner.Walk(p, fn)
}

// Copy uses the context-aware duplication capability when the driver exposes it.
func (s *boundStorage) Copy(src, dst string) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.CopyContext(s.context(), src, dst)
	}
	return s.inner.Copy(src, dst)
}

// Move uses the context-aware relocation capability when the driver exposes it.
func (s *boundStorage) Move(src, dst string) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.MoveContext(s.context(), src, dst)
	}
	return s.inner.Move(src, dst)
}

// URL uses the context-aware URL capability when the driver exposes it.
func (s *boundStorage) URL(p string) (string, error) {
	if s.bindErr != nil {
		return "", s.bindErr
	}
	if s.contextual != nil {
		return s.contextual.URLContext(s.context(), p)
	}
	return s.inner.URL(p)
}

// ListPage prefers context-aware pagination and reports unsupported drivers explicitly.
func (s *boundStorage) ListPage(p string, offset, limit int) (ListPageResult, error) {
	if s.bindErr != nil {
		return ListPageResult{}, s.bindErr
	}
	if s.cpaged != nil {
		return s.cpaged.ListPageContext(s.context(), p, offset, limit)
	}
	if s.paged != nil {
		return s.paged.ListPage(p, offset, limit)
	}
	return ListPageResult{}, ErrUnsupported
}
