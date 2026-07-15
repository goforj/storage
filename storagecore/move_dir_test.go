package storagecore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// moveTestStorage is a deterministic state machine for exercising directory
// move boundaries without hiding partial backend mutations.
type moveTestStorage struct {
	entries        map[string]Entry
	data           map[string][]byte
	walkEntries    []Entry
	walkEntriesSet bool
	walkErr        error
	calls          []string
	deleteCtxErrs  []error
	makeHook       func(*moveTestStorage, context.Context, string) error
	getHook        func(*moveTestStorage, context.Context, string) ([]byte, error)
	putHook        func(*moveTestStorage, context.Context, string, []byte) error
	deleteHook     func(*moveTestStorage, context.Context, string) error
}

// newMoveTestStorage creates a source directory with isolated mutable maps.
func newMoveTestStorage() *moveTestStorage {
	return &moveTestStorage{
		entries: map[string]Entry{"src": {Path: "src", IsDir: true}},
		data:    make(map[string][]byte),
	}
}

// MakeDirContext records directory creation and permits hooks to model partial
// success before an error is returned.
func (s *moveTestStorage) MakeDirContext(ctx context.Context, p string) error {
	s.calls = append(s.calls, "mkdir:"+p)
	if s.makeHook != nil {
		return s.makeHook(s, ctx, p)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.entries[p] = Entry{Path: p, IsDir: true}
	return nil
}

// DeleteContext records the cleanup context so tests can distinguish caller
// cancellation from the detached rollback context.
func (s *moveTestStorage) DeleteContext(ctx context.Context, p string) error {
	s.calls = append(s.calls, "delete:"+p)
	s.deleteCtxErrs = append(s.deleteCtxErrs, ctx.Err())
	if s.deleteHook != nil {
		return s.deleteHook(s, ctx, p)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.entries[p]; !ok {
		return ErrNotFound
	}
	delete(s.entries, p)
	delete(s.data, p)
	return nil
}

// StatContext returns the current state and treats absent paths consistently
// with storage drivers.
func (s *moveTestStorage) StatContext(ctx context.Context, p string) (Entry, error) {
	s.calls = append(s.calls, "stat:"+p)
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	entry, ok := s.entries[p]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	return entry, nil
}

// WalkContext emits either a scripted stream or a snapshot of source
// descendants, then returns any scripted terminal walk error.
func (s *moveTestStorage) WalkContext(ctx context.Context, p string, fn func(Entry) error) error {
	s.calls = append(s.calls, "walk:"+p)
	entries := s.walkEntries
	if !s.walkEntriesSet {
		entries = entries[:0]
		for name, entry := range s.entries {
			if strings.HasPrefix(name, p+"/") {
				entry.Path = name
				entries = append(entries, entry)
			}
		}
		slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return s.walkErr
}

// GetContext returns a cloned source object unless a hook injects a read
// failure.
func (s *moveTestStorage) GetContext(ctx context.Context, p string) ([]byte, error) {
	s.calls = append(s.calls, "get:"+p)
	if s.getHook != nil {
		return s.getHook(s, ctx, p)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := s.data[p]
	if !ok {
		return nil, ErrNotFound
	}
	return slices.Clone(data), nil
}

// PutContext writes a cloned destination object unless a hook models a
// partially committed write.
func (s *moveTestStorage) PutContext(ctx context.Context, p string, contents []byte) error {
	s.calls = append(s.calls, "put:"+p)
	if s.putHook != nil {
		return s.putHook(s, ctx, p, contents)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.entries[p] = Entry{Path: p, Size: int64(len(contents))}
	s.data[p] = slices.Clone(contents)
	return nil
}

// TestMoveDirCollectsBeforeDestinationMutation proves the entire walk is a
// preflight and that destination-exists failures do not start collection.
func TestMoveDirCollectsBeforeDestinationMutation(t *testing.T) {
	t.Run("walk failure", func(t *testing.T) {
		s := newMoveTestStorage()
		s.walkEntriesSet = true
		s.walkEntries = []Entry{{Path: "src/file.txt"}}
		s.walkErr = errors.New("walk boom")
		err := MoveDirContext(context.Background(), s, "src", "dst")
		if !errors.Is(err, s.walkErr) {
			t.Fatalf("MoveDirContext error = %v", err)
		}
		assertNoCallPrefix(t, s.calls, "mkdir:")
	})

	t.Run("destination exists", func(t *testing.T) {
		s := newMoveTestStorage()
		s.entries["dst"] = Entry{Path: "dst", IsDir: true}
		err := MoveDirContext(context.Background(), s, "src", "dst")
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("MoveDirContext error = %v", err)
		}
		assertNoCallPrefix(t, s.calls, "walk:")
		assertNoCallPrefix(t, s.calls, "mkdir:")
	})
}

// TestMoveDirRejectsInvalidWalkEntries ensures a backend cannot make the move
// escape or ambiguously overwrite its validated source tree.
func TestMoveDirRejectsInvalidWalkEntries(t *testing.T) {
	tests := map[string][]Entry{
		"duplicate":    {{Path: "src/file"}, {Path: "src/file"}},
		"outside":      {{Path: "other/file"}},
		"noncanonical": {{Path: "src/a/../file"}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			s := newMoveTestStorage()
			s.walkEntriesSet = true
			s.walkEntries = entries
			err := MoveDirContext(context.Background(), s, "src", "dst")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("MoveDirContext error = %v", err)
			}
			assertNoCallPrefix(t, s.calls, "mkdir:")
		})
	}
}

// TestMoveDirReadFailureDoesNotClaimUnwrittenTarget verifies rollback owns a
// file target only after its source read succeeds.
func TestMoveDirReadFailureDoesNotClaimUnwrittenTarget(t *testing.T) {
	s := newMoveTestStorage()
	s.entries["src/file"] = Entry{Path: "src/file"}
	s.data["src/file"] = []byte("payload")
	readErr := errors.New("read boom")
	s.getHook = func(_ *moveTestStorage, _ context.Context, p string) ([]byte, error) {
		if p == "src/file" {
			return nil, readErr
		}
		return nil, nil
	}
	err := MoveDirContext(context.Background(), s, "src", "dst")
	if !errors.Is(err, readErr) {
		t.Fatalf("MoveDirContext error = %v", err)
	}
	if got := deleteCalls(s.calls); !slices.Equal(got, []string{"delete:dst"}) {
		t.Fatalf("rollback deletes = %v", got)
	}
}

// TestMoveDirPartialPutRollsBackInReverseOrder checks that potentially partial
// targets are owned and removed from deepest/latest to the destination root.
func TestMoveDirPartialPutRollsBackInReverseOrder(t *testing.T) {
	s := newMoveTestStorage()
	s.entries["src/dir"] = Entry{Path: "src/dir", IsDir: true}
	s.entries["src/dir/a"] = Entry{Path: "src/dir/a"}
	s.entries["src/z"] = Entry{Path: "src/z"}
	s.data["src/dir/a"] = []byte("a")
	s.data["src/z"] = []byte("z")
	writeErr := errors.New("write boom")
	s.putHook = func(s *moveTestStorage, _ context.Context, p string, contents []byte) error {
		s.entries[p] = Entry{Path: p, Size: int64(len(contents))}
		s.data[p] = slices.Clone(contents)
		if p == "dst/dir/a" {
			return writeErr
		}
		return nil
	}
	err := MoveDirContext(context.Background(), s, "src", "dst")
	if !errors.Is(err, writeErr) {
		t.Fatalf("MoveDirContext error = %v", err)
	}
	want := []string{"delete:dst/dir/a", "delete:dst/z", "delete:dst/dir", "delete:dst"}
	if got := deleteCalls(s.calls); !slices.Equal(got, want) {
		t.Fatalf("rollback deletes = %v want %v", got, want)
	}
}

// TestMoveDirRollbackDetachesCancellationAndJoinsCleanupError proves a canceled
// caller cannot suppress cleanup and that cleanup failure remains observable.
func TestMoveDirRollbackDetachesCancellationAndJoinsCleanupError(t *testing.T) {
	s := newMoveTestStorage()
	s.entries["src/file"] = Entry{Path: "src/file"}
	s.data["src/file"] = []byte("payload")
	ctx, cancel := context.WithCancel(context.Background())
	writeErr := errors.New("write boom")
	cleanupErr := errors.New("cleanup boom")
	s.putHook = func(s *moveTestStorage, _ context.Context, p string, contents []byte) error {
		s.entries[p] = Entry{Path: p, Size: int64(len(contents))}
		s.data[p] = slices.Clone(contents)
		cancel()
		return writeErr
	}
	s.deleteHook = func(s *moveTestStorage, ctx context.Context, p string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "dst/file" {
			return cleanupErr
		}
		delete(s.entries, p)
		return nil
	}
	err := MoveDirContext(ctx, s, "src", "dst")
	if !errors.Is(err, writeErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("MoveDirContext error = %v", err)
	}
	for i, ctxErr := range s.deleteCtxErrs {
		if ctxErr != nil {
			t.Fatalf("rollback delete context %d error = %v", i, ctxErr)
		}
	}
}

// TestMoveDirCancellationBeforeFirstDeleteRollsBack distinguishes cancellation
// during the completed copy phase from cancellation after source mutation.
func TestMoveDirCancellationBeforeFirstDeleteRollsBack(t *testing.T) {
	t.Run("file tree", func(t *testing.T) {
		s := newMoveTestStorage()
		s.entries["src/file"] = Entry{Path: "src/file"}
		s.data["src/file"] = []byte("payload")
		ctx, cancel := context.WithCancel(context.Background())
		s.putHook = func(s *moveTestStorage, _ context.Context, p string, contents []byte) error {
			s.entries[p] = Entry{Path: p, Size: int64(len(contents))}
			s.data[p] = slices.Clone(contents)
			cancel()
			return nil
		}
		err := MoveDirContext(ctx, s, "src", "dst")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("MoveDirContext error = %v", err)
		}
		if _, ok := s.entries["src/file"]; !ok {
			t.Fatal("source was deleted before cancellation rollback")
		}
		if _, ok := s.entries["dst"]; ok {
			t.Fatal("destination root remains after cancellation rollback")
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		s := newMoveTestStorage()
		ctx, cancel := context.WithCancel(context.Background())
		s.makeHook = func(s *moveTestStorage, _ context.Context, p string) error {
			s.entries[p] = Entry{Path: p, IsDir: true}
			if p == "dst" {
				cancel()
			}
			return nil
		}
		err := MoveDirContext(ctx, s, "src", "dst")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("MoveDirContext error = %v", err)
		}
		if _, ok := s.entries["src"]; !ok {
			t.Fatal("empty source was deleted after cancellation")
		}
		if _, ok := s.entries["dst"]; ok {
			t.Fatal("empty destination remains after rollback")
		}
	})
}

// TestMoveDirCancellationAfterDeleteStartsKeepsDestination proves rollback is
// disabled once any source deletion has begun, avoiding destination data loss.
func TestMoveDirCancellationAfterDeleteStartsKeepsDestination(t *testing.T) {
	s := newMoveTestStorage()
	s.entries["src/a"] = Entry{Path: "src/a"}
	s.entries["src/b"] = Entry{Path: "src/b"}
	s.data["src/a"] = []byte("a")
	s.data["src/b"] = []byte("b")
	ctx, cancel := context.WithCancel(context.Background())
	deleted := 0
	s.deleteHook = func(s *moveTestStorage, _ context.Context, p string) error {
		if strings.HasPrefix(p, "src/") {
			delete(s.entries, p)
			delete(s.data, p)
			deleted++
			if deleted == 1 {
				cancel()
			}
			return nil
		}
		delete(s.entries, p)
		return nil
	}
	err := MoveDirContext(ctx, s, "src", "dst")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveDirContext error = %v", err)
	}
	if _, ok := s.entries["dst/a"]; !ok {
		t.Fatal("complete destination was rolled back after source deletion began")
	}
	assertNoCallPrefix(t, deleteCalls(s.calls), "delete:dst")
}

// TestMoveDirSourceDeleteFailureKeepsCompleteDestination covers a backend
// failure at the irreversible phase boundary.
func TestMoveDirSourceDeleteFailureKeepsCompleteDestination(t *testing.T) {
	s := newMoveTestStorage()
	s.entries["src/file"] = Entry{Path: "src/file"}
	s.data["src/file"] = []byte("payload")
	deleteErr := errors.New("delete boom")
	s.deleteHook = func(s *moveTestStorage, _ context.Context, p string) error {
		if p == "src/file" {
			return deleteErr
		}
		delete(s.entries, p)
		return nil
	}
	err := MoveDirContext(context.Background(), s, "src", "dst")
	if !errors.Is(err, deleteErr) {
		t.Fatalf("MoveDirContext error = %v", err)
	}
	if _, ok := s.entries["dst/file"]; !ok {
		t.Fatal("destination file missing after source delete failure")
	}
	if _, ok := s.entries["src/file"]; !ok {
		t.Fatal("failing source file unexpectedly removed")
	}
	assertNoCallPrefix(t, deleteCalls(s.calls), "delete:dst")
}

// TestMoveDirValidationAndEmptyDirectory covers root, missing, non-directory,
// same-path, and successful empty-directory semantics.
func TestMoveDirValidationAndEmptyDirectory(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		s := newMoveTestStorage()
		if err := MoveDirContext(context.Background(), s, "", "dst"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("MoveDirContext root error = %v", err)
		}
	})

	t.Run("same missing", func(t *testing.T) {
		s := newMoveTestStorage()
		if err := MoveDirContext(context.Background(), s, "missing", "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("MoveDirContext same missing error = %v", err)
		}
	})

	t.Run("same file", func(t *testing.T) {
		s := newMoveTestStorage()
		s.entries["file"] = Entry{Path: "file"}
		if err := MoveDirContext(context.Background(), s, "file", "file"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("MoveDirContext same file error = %v", err)
		}
	})

	t.Run("same directory", func(t *testing.T) {
		s := newMoveTestStorage()
		if err := MoveDirContext(context.Background(), s, "src", "src"); err != nil {
			t.Fatalf("MoveDirContext same directory: %v", err)
		}
		assertNoCallPrefix(t, s.calls, "walk:")
	})

	t.Run("empty directory", func(t *testing.T) {
		s := newMoveTestStorage()
		s.walkEntriesSet = true
		s.walkEntries = []Entry{{Path: "src", IsDir: true}, {Path: "src", IsDir: true}}
		if err := MoveDirContext(context.Background(), s, "src", "dst"); err != nil {
			t.Fatalf("MoveDirContext empty directory: %v", err)
		}
		if _, ok := s.entries["src"]; ok {
			t.Fatal("empty source remains")
		}
		if entry, ok := s.entries["dst"]; !ok || !entry.IsDir {
			t.Fatalf("empty destination = %+v exists=%v", entry, ok)
		}
	})
}

// deleteCalls returns only DeleteContext calls without changing their order.
func deleteCalls(calls []string) []string {
	var deletes []string
	for _, call := range calls {
		if strings.HasPrefix(call, "delete:") {
			deletes = append(deletes, call)
		}
	}
	return deletes
}

// assertNoCallPrefix fails when an operation family was reached unexpectedly.
func assertNoCallPrefix(t *testing.T, calls []string, prefix string) {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			t.Fatalf("unexpected call %q in %v", call, calls)
		}
	}
}
