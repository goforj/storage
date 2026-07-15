package storagetest

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/storage"
	"github.com/goforj/storage/storagecore"
)

type contractMemoryStorage struct {
	files map[string][]byte
	dirs  map[string]struct{}
}

type boundContractMemoryStorage struct {
	inner *contractMemoryStorage
	ctx   context.Context
}

// newContractMemoryStorage creates the minimal deterministic backend used to exercise the shared suite itself.
func newContractMemoryStorage() *contractMemoryStorage {
	return &contractMemoryStorage{
		files: map[string][]byte{},
		dirs:  map[string]struct{}{},
	}
}

// normalize applies the public path rules so the fixture does not weaken contract coverage.
func (s *contractMemoryStorage) normalize(p string) (string, error) {
	return storage.NormalizePath(p)
}

// WithContext returns a view whose subsequent operations observe ctx cancellation.
func (s *contractMemoryStorage) WithContext(ctx context.Context) storage.Storage {
	return &boundContractMemoryStorage{inner: s, ctx: ctx}
}

// Get returns an owned copy and classifies absent objects as ErrNotFound.
func (s *contractMemoryStorage) Get(p string) ([]byte, error) {
	normalized, err := s.normalize(p)
	if err != nil {
		return nil, err
	}
	data, ok := s.files[normalized]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

// Put stores an owned copy and materializes its logical parent directories.
func (s *contractMemoryStorage) Put(p string, contents []byte) error {
	normalized, err := s.normalize(p)
	if err != nil {
		return err
	}
	s.ensureDirChain(normalized)
	s.files[normalized] = append([]byte(nil), contents...)
	return nil
}

// MakeDir materializes the requested logical directory and its missing ancestors.
func (s *contractMemoryStorage) MakeDir(p string) error {
	normalized, err := s.normalize(p)
	if err != nil {
		return err
	}
	if normalized == "" {
		return nil
	}
	s.ensureDirChain(normalized)
	s.dirs[normalized] = struct{}{}
	return nil
}

// Delete removes files or empty directories while rejecting non-empty directory deletion.
func (s *contractMemoryStorage) Delete(p string) error {
	normalized, err := s.normalize(p)
	if err != nil {
		return err
	}
	if _, ok := s.files[normalized]; ok {
		delete(s.files, normalized)
		return nil
	}
	if _, ok := s.dirs[normalized]; ok {
		for file := range s.files {
			if strings.HasPrefix(file, normalized+"/") {
				return storage.ErrForbidden
			}
		}
		for dir := range s.dirs {
			if dir != normalized && strings.HasPrefix(dir, normalized+"/") {
				return storage.ErrForbidden
			}
		}
		delete(s.dirs, normalized)
		return nil
	}
	return storage.ErrNotFound
}

// Stat reports file size or directory identity and preserves ErrNotFound for misses.
func (s *contractMemoryStorage) Stat(p string) (storage.Entry, error) {
	normalized, err := s.normalize(p)
	if err != nil {
		return storage.Entry{}, err
	}
	data, ok := s.files[normalized]
	if ok {
		return storage.Entry{Path: normalized, Size: int64(len(data))}, nil
	}
	if _, ok := s.dirs[normalized]; ok {
		return storage.Entry{Path: normalized, IsDir: true}, nil
	}
	return storage.Entry{}, storage.ErrNotFound
}

// Exists reports file presence without treating logical directories as objects.
func (s *contractMemoryStorage) Exists(p string) (bool, error) {
	normalized, err := s.normalize(p)
	if err != nil {
		return false, err
	}
	_, ok := s.files[normalized]
	return ok, nil
}

// List returns sorted immediate children and reports a missing non-root prefix.
func (s *contractMemoryStorage) List(p string) ([]storage.Entry, error) {
	normalized, err := s.normalize(p)
	if err != nil {
		return nil, err
	}
	entries := map[string]storage.Entry{}
	prefix := normalized
	if prefix != "" {
		prefix += "/"
	}
	for file, data := range s.files {
		if prefix != "" && !strings.HasPrefix(file, prefix) {
			continue
		}
		rest := strings.TrimPrefix(file, prefix)
		if rest == file && prefix != "" {
			continue
		}
		parts := strings.Split(rest, "/")
		child := parts[0]
		full := child
		if normalized != "" {
			full = normalized + "/" + child
		}
		if len(parts) == 1 {
			entries[full] = storage.Entry{Path: full, Size: int64(len(data))}
			continue
		}
		entries[full] = storage.Entry{Path: full, IsDir: true}
	}
	for dir := range s.dirs {
		if prefix != "" && !strings.HasPrefix(dir, prefix) {
			continue
		}
		rest := strings.TrimPrefix(dir, prefix)
		if rest == dir && prefix != "" {
			continue
		}
		parts := strings.Split(rest, "/")
		child := parts[0]
		full := child
		if normalized != "" {
			full = normalized + "/" + child
		}
		entries[full] = storage.Entry{Path: full, IsDir: true}
	}
	if normalized != "" && len(entries) == 0 {
		return nil, storage.ErrNotFound
	}
	result := make([]storage.Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	slices.SortFunc(result, func(a, b storage.Entry) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	return result, nil
}

// Walk visits matching files and directories in lexical path order.
func (s *contractMemoryStorage) Walk(p string, fn func(storage.Entry) error) error {
	normalized, err := s.normalize(p)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(s.files))
	for file := range s.files {
		if normalized == "" || file == normalized || strings.HasPrefix(file, normalized+"/") {
			paths = append(paths, file)
		}
	}
	for dir := range s.dirs {
		if normalized == "" || dir == normalized || strings.HasPrefix(dir, normalized+"/") {
			paths = append(paths, dir)
		}
	}
	slices.Sort(paths)
	for _, file := range paths {
		entry := storage.Entry{Path: file}
		if data, ok := s.files[file]; ok {
			entry.Size = int64(len(data))
		} else {
			entry.IsDir = true
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	if normalized != "" && len(paths) == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// Copy preserves the source by routing an owned read through the fixture's write semantics.
func (s *contractMemoryStorage) Copy(src, dst string) error {
	data, err := s.Get(src)
	if err != nil {
		return err
	}
	return s.Put(dst, data)
}

// Move uses the shared directory algorithm for trees and copy-delete semantics for files.
func (s *contractMemoryStorage) Move(src, dst string) error {
	srcEntry, err := s.Stat(src)
	if err != nil {
		return err
	}
	if srcEntry.IsDir {
		return storagecore.MoveDirContext(context.Background(), s, src, dst)
	}
	data, err := s.Get(src)
	if err != nil {
		return err
	}
	if err := s.Put(dst, data); err != nil {
		return err
	}
	return s.Delete(src)
}

// URL returns a deterministic synthetic URL only for an existing file.
func (s *contractMemoryStorage) URL(p string) (string, error) {
	normalized, err := s.normalize(p)
	if err != nil {
		return "", err
	}
	if _, ok := s.files[normalized]; !ok {
		return "", storage.ErrNotFound
	}
	return "https://example.test/" + normalized, nil
}

// GetContext checks cancellation before delegating to the fixture read.
func (s *contractMemoryStorage) GetContext(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Get(p)
}

// PutContext checks cancellation before delegating to the fixture write.
func (s *contractMemoryStorage) PutContext(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Put(p, contents)
}

// MakeDirContext checks cancellation before creating a logical directory.
func (s *contractMemoryStorage) MakeDirContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.MakeDir(p)
}

// DeleteContext checks cancellation before deleting a fixture path.
func (s *contractMemoryStorage) DeleteContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Delete(p)
}

// StatContext checks cancellation before reading fixture metadata.
func (s *contractMemoryStorage) StatContext(ctx context.Context, p string) (storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storage.Entry{}, err
	}
	return s.Stat(p)
}

// ExistsContext checks cancellation before testing fixture file presence.
func (s *contractMemoryStorage) ExistsContext(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.Exists(p)
}

// ListContext checks cancellation before listing fixture children.
func (s *contractMemoryStorage) ListContext(ctx context.Context, p string) ([]storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.List(p)
}

// ListPage applies pagination to the deterministic background-context listing.
func (s *contractMemoryStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.ListPageContext(context.Background(), p, offset, limit)
}

// ListPageContext checks cancellation before paginating the fixture's sorted listing.
func (s *contractMemoryStorage) ListPageContext(ctx context.Context, p string, offset, limit int) (storage.ListPageResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListPageResult{}, err
	}
	entries, err := s.List(p)
	if err != nil {
		return storage.ListPageResult{}, err
	}
	return storage.PaginateEntries(entries, offset, limit), nil
}

// WalkContext checks cancellation before traversing fixture entries.
func (s *contractMemoryStorage) WalkContext(ctx context.Context, p string, fn func(storage.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Walk(p, fn)
}

// CopyContext checks cancellation before copying a fixture object.
func (s *contractMemoryStorage) CopyContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Copy(src, dst)
}

// MoveContext checks cancellation before moving a fixture object or tree.
func (s *contractMemoryStorage) MoveContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Move(src, dst)
}

// URLContext checks cancellation before resolving the fixture's synthetic URL.
func (s *contractMemoryStorage) URLContext(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.URL(p)
}

// WithContext rebinds the fixture view without mutating the shared in-memory state.
func (s *boundContractMemoryStorage) WithContext(ctx context.Context) storage.Storage {
	return &boundContractMemoryStorage{inner: s.inner, ctx: ctx}
}

// Get delegates reads through the context bound to this fixture view.
func (s *boundContractMemoryStorage) Get(p string) ([]byte, error) {
	return s.inner.GetContext(s.ctx, p)
}

// Put delegates writes through the context bound to this fixture view.
func (s *boundContractMemoryStorage) Put(p string, contents []byte) error {
	return s.inner.PutContext(s.ctx, p, contents)
}

// MakeDir delegates directory creation through the bound context.
func (s *boundContractMemoryStorage) MakeDir(p string) error {
	return s.inner.MakeDirContext(s.ctx, p)
}

// Delete delegates path removal through the bound context.
func (s *boundContractMemoryStorage) Delete(p string) error {
	return s.inner.DeleteContext(s.ctx, p)
}

// Stat delegates metadata reads through the bound context.
func (s *boundContractMemoryStorage) Stat(p string) (storage.Entry, error) {
	return s.inner.StatContext(s.ctx, p)
}

// Exists delegates presence checks through the bound context.
func (s *boundContractMemoryStorage) Exists(p string) (bool, error) {
	return s.inner.ExistsContext(s.ctx, p)
}

// List delegates child listings through the bound context.
func (s *boundContractMemoryStorage) List(p string) ([]storage.Entry, error) {
	return s.inner.ListContext(s.ctx, p)
}

// Walk delegates recursive traversal through the bound context.
func (s *boundContractMemoryStorage) Walk(p string, fn func(storage.Entry) error) error {
	return s.inner.WalkContext(s.ctx, p, fn)
}

// Copy delegates object duplication through the bound context.
func (s *boundContractMemoryStorage) Copy(src, dst string) error {
	return s.inner.CopyContext(s.ctx, src, dst)
}

// Move delegates object or tree relocation through the bound context.
func (s *boundContractMemoryStorage) Move(src, dst string) error {
	return s.inner.MoveContext(s.ctx, src, dst)
}

// URL delegates synthetic URL resolution through the bound context.
func (s *boundContractMemoryStorage) URL(p string) (string, error) {
	return s.inner.URLContext(s.ctx, p)
}

// ListPage delegates deterministic pagination through the bound context.
func (s *boundContractMemoryStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.inner.ListPageContext(s.ctx, p, offset, limit)
}

// ensureDirChain records every logical ancestor needed for directory-aware listing.
func (s *contractMemoryStorage) ensureDirChain(p string) {
	dir := path.Dir(p)
	for dir != "." && dir != "" {
		s.dirs[dir] = struct{}{}
		next := path.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
}

// ModTime reports a current timestamp only after confirming that the path exists.
func (s *contractMemoryStorage) ModTime(_ context.Context, p string) (time.Time, error) {
	if _, err := s.Stat(p); err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC(), nil
}

type unsupportedStorage struct {
	inner *contractMemoryStorage
}

type boundUnsupportedStorage struct {
	inner *contractMemoryStorage
	ctx   context.Context
}

// WithContext binds required operations while retaining deliberately unsupported optional methods.
func (s unsupportedStorage) WithContext(ctx context.Context) storage.Storage {
	return boundUnsupportedStorage{inner: s.inner, ctx: ctx}
}

// Get preserves the required read behavior of the inner fixture.
func (s unsupportedStorage) Get(p string) ([]byte, error) { return s.inner.Get(p) }

// Put preserves the required write behavior of the inner fixture.
func (s unsupportedStorage) Put(p string, contents []byte) error { return s.inner.Put(p, contents) }

// MakeDir preserves the required directory behavior of the inner fixture.
func (s unsupportedStorage) MakeDir(p string) error { return s.inner.MakeDir(p) }

// Delete preserves the required removal behavior of the inner fixture.
func (s unsupportedStorage) Delete(p string) error { return s.inner.Delete(p) }

// Stat preserves the required metadata behavior of the inner fixture.
func (s unsupportedStorage) Stat(p string) (storage.Entry, error) { return s.inner.Stat(p) }

// Exists preserves the required presence behavior of the inner fixture.
func (s unsupportedStorage) Exists(p string) (bool, error) { return s.inner.Exists(p) }

// List preserves the required listing behavior of the inner fixture.
func (s unsupportedStorage) List(p string) ([]storage.Entry, error) { return s.inner.List(p) }

// ListPage preserves deterministic pagination while optional methods remain unsupported.
func (s unsupportedStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.inner.ListPageContext(context.Background(), p, offset, limit)
}

// Copy preserves the required copy behavior of the inner fixture.
func (s unsupportedStorage) Copy(src, dst string) error { return s.inner.Copy(src, dst) }

// Move preserves the required move behavior of the inner fixture.
func (s unsupportedStorage) Move(src, dst string) error { return s.inner.Move(src, dst) }

// Walk returns ErrUnsupported so the shared suite exercises its optional-operation branch.
func (s unsupportedStorage) Walk(string, func(storage.Entry) error) error {
	return storage.ErrUnsupported
}

// URL returns ErrUnsupported so the shared suite exercises its optional-operation branch.
func (s unsupportedStorage) URL(string) (string, error) {
	return "", storage.ErrUnsupported
}

// WithContext rebinds required operations while retaining unsupported optional methods.
func (s boundUnsupportedStorage) WithContext(ctx context.Context) storage.Storage {
	return boundUnsupportedStorage{inner: s.inner, ctx: ctx}
}

// Get delegates required reads through the bound context.
func (s boundUnsupportedStorage) Get(p string) ([]byte, error) { return s.inner.GetContext(s.ctx, p) }

// Put delegates required writes through the bound context.
func (s boundUnsupportedStorage) Put(p string, contents []byte) error {
	return s.inner.PutContext(s.ctx, p, contents)
}

// MakeDir delegates required directory creation through the bound context.
func (s boundUnsupportedStorage) MakeDir(p string) error { return s.inner.MakeDirContext(s.ctx, p) }

// Delete delegates required removal through the bound context.
func (s boundUnsupportedStorage) Delete(p string) error { return s.inner.DeleteContext(s.ctx, p) }

// Stat delegates required metadata reads through the bound context.
func (s boundUnsupportedStorage) Stat(p string) (storage.Entry, error) {
	return s.inner.StatContext(s.ctx, p)
}

// Exists delegates required presence checks through the bound context.
func (s boundUnsupportedStorage) Exists(p string) (bool, error) {
	return s.inner.ExistsContext(s.ctx, p)
}

// List delegates required child listings through the bound context.
func (s boundUnsupportedStorage) List(p string) ([]storage.Entry, error) {
	return s.inner.ListContext(s.ctx, p)
}

// ListPage delegates deterministic pagination through the bound context.
func (s boundUnsupportedStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.inner.ListPageContext(s.ctx, p, offset, limit)
}

// Copy delegates required duplication through the bound context.
func (s boundUnsupportedStorage) Copy(src, dst string) error {
	return s.inner.CopyContext(s.ctx, src, dst)
}

// Move delegates required relocation through the bound context.
func (s boundUnsupportedStorage) Move(src, dst string) error {
	return s.inner.MoveContext(s.ctx, src, dst)
}

// Walk gives cancellation precedence before returning the optional-operation sentinel.
func (s boundUnsupportedStorage) Walk(string, func(storage.Entry) error) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return storage.ErrUnsupported
}

// URL gives cancellation precedence before returning the optional-operation sentinel.
func (s boundUnsupportedStorage) URL(string) (string, error) {
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	return "", storage.ErrUnsupported
}

// TestRunStorageContractTests verifies that the shared suite accepts a complete conforming backend.
func TestRunStorageContractTests(t *testing.T) {
	RunStorageContractTests(t, newContractMemoryStorage())
}

// TestRunStorageContractTestsWithUnsupportedOptionals verifies that optional Walk and URL sentinels are accepted.
func TestRunStorageContractTestsWithUnsupportedOptionals(t *testing.T) {
	RunStorageContractTests(t, unsupportedStorage{inner: newContractMemoryStorage()})
}

// TestExtractPaths verifies path projection without reordering entries.
func TestExtractPaths(t *testing.T) {
	entries := []storage.Entry{
		{Path: "a"},
		{Path: "b"},
	}
	if got := extractPaths(entries); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("extractPaths = %v", got)
	}
}

// TestContractMemoryStorageListMissingRoot verifies the fixture preserves ErrNotFound for absent prefixes.
func TestContractMemoryStorageListMissingRoot(t *testing.T) {
	store := newContractMemoryStorage()
	if _, err := store.List("missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("List missing error = %v", err)
	}
}
