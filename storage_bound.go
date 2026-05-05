package storage

import (
	"context"

	storagecore "github.com/goforj/storage/storagecore"
)

type boundStorage struct {
	inner      storagecore.Storage
	contextual storagecore.ContextStorage
	paged      storagecore.PagedStorage
	cpaged     storagecore.ContextPagedStorage
	ctx        context.Context
}

func wrapStorage(inner storagecore.Storage) Storage {
	wrapped := &boundStorage{
		inner: inner,
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

func (s *boundStorage) WithContext(ctx context.Context) Storage {
	clone := *s
	clone.ctx = ctx
	return &clone
}

func (s *boundStorage) context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

func (s *boundStorage) Get(p string) ([]byte, error) {
	if s.contextual != nil {
		return s.contextual.GetContext(s.context(), p)
	}
	return s.inner.Get(p)
}

func (s *boundStorage) Put(p string, contents []byte) error {
	if s.contextual != nil {
		return s.contextual.PutContext(s.context(), p, contents)
	}
	return s.inner.Put(p, contents)
}

func (s *boundStorage) MakeDir(p string) error {
	if s.contextual != nil {
		return s.contextual.MakeDirContext(s.context(), p)
	}
	return s.inner.MakeDir(p)
}

func (s *boundStorage) Delete(p string) error {
	if s.contextual != nil {
		return s.contextual.DeleteContext(s.context(), p)
	}
	return s.inner.Delete(p)
}

func (s *boundStorage) Stat(p string) (Entry, error) {
	if s.contextual != nil {
		return s.contextual.StatContext(s.context(), p)
	}
	return s.inner.Stat(p)
}

func (s *boundStorage) Exists(p string) (bool, error) {
	if s.contextual != nil {
		return s.contextual.ExistsContext(s.context(), p)
	}
	return s.inner.Exists(p)
}

func (s *boundStorage) List(p string) ([]Entry, error) {
	if s.contextual != nil {
		return s.contextual.ListContext(s.context(), p)
	}
	return s.inner.List(p)
}

func (s *boundStorage) Walk(p string, fn func(Entry) error) error {
	if s.contextual != nil {
		return s.contextual.WalkContext(s.context(), p, fn)
	}
	return s.inner.Walk(p, fn)
}

func (s *boundStorage) Copy(src, dst string) error {
	if s.contextual != nil {
		return s.contextual.CopyContext(s.context(), src, dst)
	}
	return s.inner.Copy(src, dst)
}

func (s *boundStorage) Move(src, dst string) error {
	if s.contextual != nil {
		return s.contextual.MoveContext(s.context(), src, dst)
	}
	return s.inner.Move(src, dst)
}

func (s *boundStorage) URL(p string) (string, error) {
	if s.contextual != nil {
		return s.contextual.URLContext(s.context(), p)
	}
	return s.inner.URL(p)
}

func (s *boundStorage) ListPage(p string, offset, limit int) (ListPageResult, error) {
	if s.cpaged != nil {
		return s.cpaged.ListPageContext(s.context(), p, offset, limit)
	}
	if s.paged != nil {
		return s.paged.ListPage(p, offset, limit)
	}
	return ListPageResult{}, ErrUnsupported
}
