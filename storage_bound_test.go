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

// TestRegisteredPublicContextBinderIsPreserved verifies custom binding and one shared Close survive registry adaptation.
func TestRegisteredPublicContextBinderIsPreserved(t *testing.T) {
	name := fmt.Sprintf("binder-only-%s", t.Name())
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
