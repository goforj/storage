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
	storagecore "github.com/goforj/storage/storagecore"
)

type contractMemoryStorage struct {
	files map[string][]byte
	dirs  map[string]struct{}
}

type boundContractMemoryStorage struct {
	inner *contractMemoryStorage
	ctx   context.Context
}

func newContractMemoryStorage() *contractMemoryStorage {
	return &contractMemoryStorage{
		files: map[string][]byte{},
		dirs:  map[string]struct{}{},
	}
}

func (s *contractMemoryStorage) normalize(p string) (string, error) {
	return storage.NormalizePath(p)
}

func (s *contractMemoryStorage) WithContext(ctx context.Context) storage.Storage {
	return &boundContractMemoryStorage{inner: s, ctx: ctx}
}

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

func (s *contractMemoryStorage) Put(p string, contents []byte) error {
	normalized, err := s.normalize(p)
	if err != nil {
		return err
	}
	s.ensureDirChain(normalized)
	s.files[normalized] = append([]byte(nil), contents...)
	return nil
}

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

func (s *contractMemoryStorage) Exists(p string) (bool, error) {
	normalized, err := s.normalize(p)
	if err != nil {
		return false, err
	}
	_, ok := s.files[normalized]
	return ok, nil
}

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

func (s *contractMemoryStorage) Copy(src, dst string) error {
	data, err := s.Get(src)
	if err != nil {
		return err
	}
	return s.Put(dst, data)
}

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

func (s *contractMemoryStorage) GetContext(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Get(p)
}

func (s *contractMemoryStorage) PutContext(ctx context.Context, p string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Put(p, contents)
}

func (s *contractMemoryStorage) MakeDirContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.MakeDir(p)
}

func (s *contractMemoryStorage) DeleteContext(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Delete(p)
}

func (s *contractMemoryStorage) StatContext(ctx context.Context, p string) (storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return storage.Entry{}, err
	}
	return s.Stat(p)
}

func (s *contractMemoryStorage) ExistsContext(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.Exists(p)
}

func (s *contractMemoryStorage) ListContext(ctx context.Context, p string) ([]storage.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.List(p)
}

func (s *contractMemoryStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.ListPageContext(context.Background(), p, offset, limit)
}

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

func (s *contractMemoryStorage) WalkContext(ctx context.Context, p string, fn func(storage.Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Walk(p, fn)
}

func (s *contractMemoryStorage) CopyContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Copy(src, dst)
}

func (s *contractMemoryStorage) MoveContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Move(src, dst)
}

func (s *contractMemoryStorage) URLContext(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.URL(p)
}

func (s *boundContractMemoryStorage) WithContext(ctx context.Context) storage.Storage {
	return &boundContractMemoryStorage{inner: s.inner, ctx: ctx}
}

func (s *boundContractMemoryStorage) Get(p string) ([]byte, error) {
	return s.inner.GetContext(s.ctx, p)
}

func (s *boundContractMemoryStorage) Put(p string, contents []byte) error {
	return s.inner.PutContext(s.ctx, p, contents)
}

func (s *boundContractMemoryStorage) MakeDir(p string) error {
	return s.inner.MakeDirContext(s.ctx, p)
}

func (s *boundContractMemoryStorage) Delete(p string) error {
	return s.inner.DeleteContext(s.ctx, p)
}

func (s *boundContractMemoryStorage) Stat(p string) (storage.Entry, error) {
	return s.inner.StatContext(s.ctx, p)
}

func (s *boundContractMemoryStorage) Exists(p string) (bool, error) {
	return s.inner.ExistsContext(s.ctx, p)
}

func (s *boundContractMemoryStorage) List(p string) ([]storage.Entry, error) {
	return s.inner.ListContext(s.ctx, p)
}

func (s *boundContractMemoryStorage) Walk(p string, fn func(storage.Entry) error) error {
	return s.inner.WalkContext(s.ctx, p, fn)
}

func (s *boundContractMemoryStorage) Copy(src, dst string) error {
	return s.inner.CopyContext(s.ctx, src, dst)
}

func (s *boundContractMemoryStorage) Move(src, dst string) error {
	return s.inner.MoveContext(s.ctx, src, dst)
}

func (s *boundContractMemoryStorage) URL(p string) (string, error) {
	return s.inner.URLContext(s.ctx, p)
}

func (s *boundContractMemoryStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.inner.ListPageContext(s.ctx, p, offset, limit)
}

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

func (s unsupportedStorage) WithContext(ctx context.Context) storage.Storage {
	return boundUnsupportedStorage{inner: s.inner, ctx: ctx}
}
func (s unsupportedStorage) Get(p string) ([]byte, error)           { return s.inner.Get(p) }
func (s unsupportedStorage) Put(p string, contents []byte) error    { return s.inner.Put(p, contents) }
func (s unsupportedStorage) MakeDir(p string) error                 { return s.inner.MakeDir(p) }
func (s unsupportedStorage) Delete(p string) error                  { return s.inner.Delete(p) }
func (s unsupportedStorage) Stat(p string) (storage.Entry, error)   { return s.inner.Stat(p) }
func (s unsupportedStorage) Exists(p string) (bool, error)          { return s.inner.Exists(p) }
func (s unsupportedStorage) List(p string) ([]storage.Entry, error) { return s.inner.List(p) }
func (s unsupportedStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.inner.ListPageContext(context.Background(), p, offset, limit)
}
func (s unsupportedStorage) Copy(src, dst string) error { return s.inner.Copy(src, dst) }
func (s unsupportedStorage) Move(src, dst string) error { return s.inner.Move(src, dst) }
func (s unsupportedStorage) Walk(string, func(storage.Entry) error) error {
	return storage.ErrUnsupported
}
func (s unsupportedStorage) URL(string) (string, error) {
	return "", storage.ErrUnsupported
}

func (s boundUnsupportedStorage) WithContext(ctx context.Context) storage.Storage {
	return boundUnsupportedStorage{inner: s.inner, ctx: ctx}
}

func (s boundUnsupportedStorage) Get(p string) ([]byte, error) { return s.inner.GetContext(s.ctx, p) }
func (s boundUnsupportedStorage) Put(p string, contents []byte) error {
	return s.inner.PutContext(s.ctx, p, contents)
}
func (s boundUnsupportedStorage) MakeDir(p string) error { return s.inner.MakeDirContext(s.ctx, p) }
func (s boundUnsupportedStorage) Delete(p string) error  { return s.inner.DeleteContext(s.ctx, p) }
func (s boundUnsupportedStorage) Stat(p string) (storage.Entry, error) {
	return s.inner.StatContext(s.ctx, p)
}
func (s boundUnsupportedStorage) Exists(p string) (bool, error) { return s.inner.ExistsContext(s.ctx, p) }
func (s boundUnsupportedStorage) List(p string) ([]storage.Entry, error) {
	return s.inner.ListContext(s.ctx, p)
}
func (s boundUnsupportedStorage) ListPage(p string, offset, limit int) (storage.ListPageResult, error) {
	return s.inner.ListPageContext(s.ctx, p, offset, limit)
}
func (s boundUnsupportedStorage) Copy(src, dst string) error { return s.inner.CopyContext(s.ctx, src, dst) }
func (s boundUnsupportedStorage) Move(src, dst string) error { return s.inner.MoveContext(s.ctx, src, dst) }
func (s boundUnsupportedStorage) Walk(string, func(storage.Entry) error) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return storage.ErrUnsupported
}
func (s boundUnsupportedStorage) URL(string) (string, error) {
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	return "", storage.ErrUnsupported
}

func TestRunStorageContractTests(t *testing.T) {
	RunStorageContractTests(t, newContractMemoryStorage())
}

func TestRunStorageContractTestsWithUnsupportedOptionals(t *testing.T) {
	RunStorageContractTests(t, unsupportedStorage{inner: newContractMemoryStorage()})
}

func TestExtractPaths(t *testing.T) {
	entries := []storage.Entry{
		{Path: "a"},
		{Path: "b"},
	}
	if got := extractPaths(entries); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("extractPaths = %v", got)
	}
}

func TestContractMemoryStorageListMissingRoot(t *testing.T) {
	store := newContractMemoryStorage()
	if _, err := store.List("missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("List missing error = %v", err)
	}
}
