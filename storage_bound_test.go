package storage

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

type binderOnlyStorage struct {
	stubFS
	ctx    context.Context
	closes *atomic.Int32
}

// WithContext is the only context-aware surface exposed by binderOnlyStorage.
func (s *binderOnlyStorage) WithContext(ctx context.Context) Storage {
	clone := *s
	clone.ctx = ctx
	return &clone
}

// Get proves the public binder was invoked by observing its bound context.
func (s *binderOnlyStorage) Get(string) ([]byte, error) {
	if s.ctx == nil {
		return nil, nil
	}
	return nil, s.ctx.Err()
}

// Close records lifecycle delegation across context-derived handles.
func (s *binderOnlyStorage) Close() error {
	s.closes.Add(1)
	return nil
}

type nilBinderStorage struct{ stubFS }

// WithContext deliberately returns nil to exercise invalid binder handling.
func (nilBinderStorage) WithContext(context.Context) Storage { return nil }

type typedNilWalker struct{}

// Walk must never be reached for a typed-nil CountFiles input.
func (*typedNilWalker) Walk(string, func(Entry) error) error {
	panic("Walk called on typed-nil input")
}

type coreStub struct{}

// Get returns an empty core payload.
func (coreStub) Get(string) ([]byte, error) { return nil, nil }

// Put accepts core writes.
func (coreStub) Put(string, []byte) error { return nil }

// MakeDir accepts core directory creation.
func (coreStub) MakeDir(string) error { return nil }

// Delete accepts core deletion.
func (coreStub) Delete(string) error { return nil }

// Stat returns empty core metadata.
func (coreStub) Stat(string) (Entry, error) { return Entry{}, nil }

// Exists returns a deterministic core result.
func (coreStub) Exists(string) (bool, error) { return true, nil }

// List returns an empty core listing.
func (coreStub) List(string) ([]Entry, error) { return nil, nil }

// Walk reports unsupported core traversal.
func (coreStub) Walk(string, func(Entry) error) error { return ErrUnsupported }

// Copy accepts core duplication.
func (coreStub) Copy(string, string) error { return nil }

// Move accepts core relocation.
func (coreStub) Move(string, string) error { return nil }

// URL returns an empty core address.
func (coreStub) URL(string) (string, error) { return "", nil }

type contextDispatchStorage struct {
	coreStub
	calls *[]string
}

// record captures the context method selected by the bound adapter.
func (s *contextDispatchStorage) record(ctx context.Context, operation string) error {
	*s.calls = append(*s.calls, operation)
	return ctx.Err()
}

// GetContext records context-aware reads.
func (s *contextDispatchStorage) GetContext(ctx context.Context, _ string) ([]byte, error) {
	return nil, s.record(ctx, "get")
}

// PutContext records context-aware writes.
func (s *contextDispatchStorage) PutContext(ctx context.Context, _ string, _ []byte) error {
	return s.record(ctx, "put")
}

// MakeDirContext records context-aware directory creation.
func (s *contextDispatchStorage) MakeDirContext(ctx context.Context, _ string) error {
	return s.record(ctx, "mkdir")
}

// DeleteContext records context-aware deletion.
func (s *contextDispatchStorage) DeleteContext(ctx context.Context, _ string) error {
	return s.record(ctx, "delete")
}

// StatContext records context-aware metadata lookup.
func (s *contextDispatchStorage) StatContext(ctx context.Context, _ string) (Entry, error) {
	return Entry{}, s.record(ctx, "stat")
}

// ExistsContext records context-aware existence checks.
func (s *contextDispatchStorage) ExistsContext(ctx context.Context, _ string) (bool, error) {
	return false, s.record(ctx, "exists")
}

// ListContext records context-aware one-level listing.
func (s *contextDispatchStorage) ListContext(ctx context.Context, _ string) ([]Entry, error) {
	return nil, s.record(ctx, "list")
}

// WalkContext records context-aware recursive traversal.
func (s *contextDispatchStorage) WalkContext(ctx context.Context, _ string, _ func(Entry) error) error {
	return s.record(ctx, "walk")
}

// CopyContext records context-aware copying.
func (s *contextDispatchStorage) CopyContext(ctx context.Context, _, _ string) error {
	return s.record(ctx, "copy")
}

// MoveContext records context-aware relocation.
func (s *contextDispatchStorage) MoveContext(ctx context.Context, _, _ string) error {
	return s.record(ctx, "move")
}

// URLContext records context-aware URL generation.
func (s *contextDispatchStorage) URLContext(ctx context.Context, _ string) (string, error) {
	return "", s.record(ctx, "url")
}

type pagedStorage struct{ coreStub }

// ListPage returns a marker proving synchronous pagination dispatch.
func (pagedStorage) ListPage(string, int, int) (ListPageResult, error) {
	return ListPageResult{Offset: 11}, nil
}

type contextPagedStorage struct{ pagedStorage }

// ListPageContext returns a marker proving context pagination takes precedence.
func (contextPagedStorage) ListPageContext(context.Context, string, int, int) (ListPageResult, error) {
	return ListPageResult{Offset: 22}, nil
}

type basicCounterWalker struct {
	entries []Entry
	err     error
}

// Walk emits configured entries before returning its terminal error.
func (w basicCounterWalker) Walk(_ string, fn func(Entry) error) error {
	for _, entry := range w.entries {
		if err := fn(entry); err != nil {
			return err
		}
	}
	return w.err
}

type contextCounterWalker struct {
	entries []Entry
}

// WalkContext emits configured entries through the caller's context.
func (w contextCounterWalker) WalkContext(ctx context.Context, _ string, fn func(Entry) error) error {
	for _, entry := range w.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return nil
}

// TestRegisteredPublicContextBinderIsPreserved verifies custom binding and one shared Close survive registry adaptation.
func TestRegisteredPublicContextBinderIsPreserved(t *testing.T) {
	name := uniqueTestDriverName("binder-only")
	var closes atomic.Int32
	RegisterDriver(name, func(context.Context, ResolvedConfig) (Storage, error) {
		return &binderOnlyStorage{closes: &closes}, nil
	})
	store, err := Build(fakeDriverConfig{name: name})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	derived := store.WithContext(ctx)
	if _, err := derived.Get("file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("derived Get error = %v", err)
	}
	if err := derived.(interface{ Close() error }).Close(); err != nil {
		t.Fatalf("derived Close: %v", err)
	}
	if err := store.(interface{ Close() error }).Close(); err != nil {
		t.Fatalf("store Close: %v", err)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("underlying Close calls = %d want 1", got)
	}
}

// TestNilPublicContextBinderDoesNotFallBack exposes an invalid custom binder instead of silently changing semantics.
func TestNilPublicContextBinderDoesNotFallBack(t *testing.T) {
	store := wrapStorage(nilBinderStorage{})
	bound := store.WithContext(context.Background())
	if _, err := bound.Get("file.txt"); err == nil || err.Error() != "storage: WithContext returned a nil storage" {
		t.Fatalf("bound Get error = %v", err)
	}
}

// TestCountFilesRejectsNilWalkers covers nil interfaces, typed nils, and invalid context binders.
func TestCountFilesRejectsNilWalkers(t *testing.T) {
	if _, err := CountFilesContext(context.Background(), nil, ""); err == nil || err.Error() != "storage: count files requires a non-nil disk" {
		t.Fatalf("nil disk error = %v", err)
	}
	var walker *typedNilWalker
	if _, err := CountFilesContext(context.Background(), walker, ""); err == nil || err.Error() != "storage: count files requires a non-nil disk" {
		t.Fatalf("typed-nil disk error = %v", err)
	}
	if _, err := CountFilesContext(context.Background(), nilBinderStorage{}, ""); err == nil || err.Error() != "storage: WithContext returned a nil storage" {
		t.Fatalf("nil binder error = %v", err)
	}
}

// TestCountFilesWalkerCapabilities verifies context, basic, unsupported, and failure paths.
func TestCountFilesWalkerCapabilities(t *testing.T) {
	entries := []Entry{{Path: "dir", IsDir: true}, {Path: "a"}, {Path: "b"}}
	total, err := CountFilesContext(nil, contextCounterWalker{entries: entries}, "")
	if err != nil || total != 2 {
		t.Fatalf("context walker count = %d err=%v", total, err)
	}
	total, err = CountFilesContext(context.Background(), basicCounterWalker{entries: entries}, "")
	if err != nil || total != 2 {
		t.Fatalf("basic walker count = %d err=%v", total, err)
	}
	if _, err := CountFilesContext(context.Background(), struct{}{}, ""); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported walker error = %v", err)
	}
	walkErr := errors.New("walk failure")
	if _, err := CountFilesContext(context.Background(), basicCounterWalker{err: walkErr}, ""); !errors.Is(err, walkErr) {
		t.Fatalf("basic walker error = %v", err)
	}
}

// TestBoundStorageDispatch covers synchronous fallback and context-aware operation routing.
func TestBoundStorageDispatch(t *testing.T) {
	t.Run("synchronous fallback", func(t *testing.T) {
		store := wrapStorage(stubFS{})
		if _, err := store.Get("file"); err != nil {
			t.Fatalf("Get: %v", err)
		}
		if err := store.Put("file", nil); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := store.MakeDir("dir"); err != nil {
			t.Fatalf("MakeDir: %v", err)
		}
		if err := store.Delete("file"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.Stat("file"); err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if _, err := store.Exists("file"); err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if _, err := store.List(""); err != nil {
			t.Fatalf("List: %v", err)
		}
		if err := store.Walk("", func(Entry) error { return nil }); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Walk error = %v", err)
		}
		if err := store.Copy("a", "b"); err != nil {
			t.Fatalf("Copy: %v", err)
		}
		if err := store.Move("a", "b"); err != nil {
			t.Fatalf("Move: %v", err)
		}
		if _, err := store.URL("file"); err != nil {
			t.Fatalf("URL: %v", err)
		}
		if _, err := store.(PagedStorage).ListPage("", 0, 1); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ListPage error = %v", err)
		}
	})

	t.Run("context aware", func(t *testing.T) {
		var calls []string
		store := wrapStorage(&contextDispatchStorage{calls: &calls}).WithContext(nil)
		_, _ = store.Get("file")
		_ = store.Put("file", nil)
		_ = store.MakeDir("dir")
		_ = store.Delete("file")
		_, _ = store.Stat("file")
		_, _ = store.Exists("file")
		_, _ = store.List("")
		_ = store.Walk("", func(Entry) error { return nil })
		_ = store.Copy("a", "b")
		_ = store.Move("a", "b")
		_, _ = store.URL("file")

		want := []string{"get", "put", "mkdir", "delete", "stat", "exists", "list", "walk", "copy", "move", "url"}
		if fmt.Sprint(calls) != fmt.Sprint(want) {
			t.Fatalf("context calls = %v want %v", calls, want)
		}
	})
}

// TestBoundStorageBindErrorsReachEveryOperation verifies invalid binders fail consistently.
func TestBoundStorageBindErrorsReachEveryOperation(t *testing.T) {
	store := wrapStorage(nilBinderStorage{}).WithContext(context.Background())
	assertBindErr := func(name string, err error) {
		t.Helper()
		if err == nil || err.Error() != "storage: WithContext returned a nil storage" {
			t.Errorf("%s error = %v", name, err)
		}
	}
	_, err := store.Get("file")
	assertBindErr("Get", err)
	assertBindErr("Put", store.Put("file", nil))
	assertBindErr("MakeDir", store.MakeDir("dir"))
	assertBindErr("Delete", store.Delete("file"))
	_, err = store.Stat("file")
	assertBindErr("Stat", err)
	_, err = store.Exists("file")
	assertBindErr("Exists", err)
	_, err = store.List("")
	assertBindErr("List", err)
	assertBindErr("Walk", store.Walk("", func(Entry) error { return nil }))
	assertBindErr("Copy", store.Copy("a", "b"))
	assertBindErr("Move", store.Move("a", "b"))
	_, err = store.URL("file")
	assertBindErr("URL", err)
	_, err = store.(PagedStorage).ListPage("", 0, 1)
	assertBindErr("ListPage", err)
}

// TestBoundStoragePagination verifies context pagination precedence and synchronous fallback.
func TestBoundStoragePagination(t *testing.T) {
	synchronous := wrapStorage(pagedStorage{}).(PagedStorage)
	page, err := synchronous.ListPage("", 0, 1)
	if err != nil || page.Offset != 11 {
		t.Fatalf("synchronous ListPage = %+v err=%v", page, err)
	}

	contextual := wrapStorage(contextPagedStorage{}).WithContext(context.Background()).(PagedStorage)
	page, err = contextual.ListPage("", 0, 1)
	if err != nil || page.Offset != 22 {
		t.Fatalf("context ListPage = %+v err=%v", page, err)
	}
}

// TestPaginateEntries verifies the public pagination facade clamps and isolates results.
func TestPaginateEntries(t *testing.T) {
	entries := []Entry{{Path: "a"}, {Path: "b"}, {Path: "c"}}
	page := PaginateEntries(entries, -4, 2)
	if page.Offset != 0 || page.Limit != 2 || !page.HasMore || len(page.Entries) != 2 {
		t.Fatalf("PaginateEntries result = %+v", page)
	}
	page.Entries[0].Path = "changed"
	if entries[0].Path != "a" {
		t.Fatalf("PaginateEntries mutated caller entries = %+v", entries)
	}
}
