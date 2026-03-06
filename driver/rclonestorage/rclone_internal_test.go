package rclonestorage

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/fs/hash"

	"github.com/goforj/storage"
)

func TestInitRcloneConfigData(t *testing.T) {
	resetRcloneInit(t)

	conf, err := RenderLocal(LocalRemote{Name: "localdisk"})
	if err != nil {
		t.Fatalf("RenderLocal: %v", err)
	}

	if err := initRclone(storage.ResolvedConfig{RcloneConfigData: conf}); err != nil {
		t.Fatalf("initRclone: %v", err)
	}

	if initConfigPath != "inline-rclone.conf" {
		t.Fatalf("expected inline config path, got %q", initConfigPath)
	}
	if initConfigData != conf {
		t.Fatalf("expected config data to be captured")
	}
	if config.Data() == nil {
		t.Fatalf("expected config storage to be set")
	}
	if _, ok := config.Data().(*memoryStorage); !ok {
		t.Fatalf("expected memory storage, got %T", config.Data())
	}
}

func TestInitRcloneConfigDataConflict(t *testing.T) {
	resetRcloneInit(t)

	err := initRclone(storage.ResolvedConfig{
		RcloneConfigData: "data",
		RcloneConfigPath: "path",
	})
	if err == nil {
		t.Fatalf("expected error for config path and data conflict")
	}
}

func TestInitRcloneConfigDataSetConfigPathError(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("set config path failed")
	setConfigPath = func(string) error {
		return sentinel
	}
	t.Cleanup(func() {
		setConfigPath = config.SetConfigPath
	})

	err := initRclone(storage.ResolvedConfig{RcloneConfigData: "[one]\ntype = local\n"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected setConfigPath error, got %v", err)
	}
}

func TestInitRcloneConfigPathConflict(t *testing.T) {
	resetRcloneInit(t)

	path1 := filepath.Join(t.TempDir(), "rclone-one.conf")
	path2 := filepath.Join(t.TempDir(), "rclone-two.conf")

	if err := initRclone(storage.ResolvedConfig{RcloneConfigPath: path1}); err != nil {
		t.Fatalf("initRclone path1: %v", err)
	}
	if err := initRclone(storage.ResolvedConfig{RcloneConfigPath: path2}); err == nil {
		t.Fatalf("expected error for conflicting config path")
	}
}

func TestInitRcloneConfigPathSetConfigPathError(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("set config path failed")
	setConfigPath = func(string) error {
		return sentinel
	}
	t.Cleanup(func() {
		setConfigPath = config.SetConfigPath
	})

	err := initRclone(storage.ResolvedConfig{RcloneConfigPath: "badpath.conf"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected setConfigPath error, got %v", err)
	}
}

func TestInitRcloneReturnsInitErr(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("boom")
	initErr = sentinel

	if err := initRclone(storage.ResolvedConfig{}); !errors.Is(err, sentinel) {
		t.Fatalf("expected initErr to be returned, got %v", err)
	}
}

func TestInitRcloneEmptyConfig(t *testing.T) {
	resetRcloneInit(t)

	if err := initRclone(storage.ResolvedConfig{}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestNewMissingRemote(t *testing.T) {
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatalf("expected error for missing remote")
	}
}

func TestNewInitRcloneError(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("init failed")
	initErr = sentinel

	_, err := New(context.Background(), Config{Remote: "localdisk:"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected init error, got %v", err)
	}
}

func TestNewCreateFsError(t *testing.T) {
	resetRcloneInit(t)

	conf, err := RenderLocal(LocalRemote{Name: "localdisk"})
	if err != nil {
		t.Fatalf("RenderLocal: %v", err)
	}

	_, err = New(
		context.Background(),
		Config{Remote: "missing:", RcloneConfigData: conf},
	)
	if err == nil {
		t.Fatalf("expected error for missing remote config")
	}
}

func TestDriverContextErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &driver{}

	if _, err := d.Get(ctx, "file.txt"); err == nil {
		t.Fatalf("expected Get to return context error")
	}
	if err := d.Put(ctx, "file.txt", []byte("x")); err == nil {
		t.Fatalf("expected Put to return context error")
	}
	if err := d.Delete(ctx, "file.txt"); err == nil {
		t.Fatalf("expected Delete to return context error")
	}
	if _, err := d.Exists(ctx, "file.txt"); err == nil {
		t.Fatalf("expected Exists to return context error")
	}
	if _, err := d.List(ctx, ""); err == nil {
		t.Fatalf("expected List to return context error")
	}
	if _, err := d.URL(ctx, "file.txt"); err == nil {
		t.Fatalf("expected URL to return context error")
	}
}

func TestDriverGetNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if _, err := d.Get(context.Background(), "missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDriverGetInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.Get(context.Background(), "../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestDriverFullPathInvalid(t *testing.T) {
	d := &driver{prefix: "root"}
	if _, err := d.fullPath("../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestDriverListPrefixEntry(t *testing.T) {
	fake := newFakeFs()
	fake.listEntries = fs.DirEntries{
		&fakeDirectory{fakeDirEntry: fakeDirEntry{remote: "prefix", fsys: fake}},
	}

	d := &driver{fs: fake, prefix: "prefix"}
	entries, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Path != "prefix" {
		t.Fatalf("expected entry path to be prefix, got %q", entries[0].Path)
	}
	if !entries[0].IsDir {
		t.Fatalf("expected entry to be directory")
	}
}

func TestDriverListInvalidPath(t *testing.T) {
	fake := newFakeFs()
	d := &driver{fs: fake, prefix: "root"}
	if _, err := d.List(context.Background(), "../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestDriverURLNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.features.PublicLink = func(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
		return "", fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if _, err := d.URL(context.Background(), "missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDriverURLUnsupported(t *testing.T) {
	d := &driver{fs: newFakeFs()}
	if _, err := d.URL(context.Background(), "file.txt"); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestDriverURLForbidden(t *testing.T) {
	fake := newFakeFs()
	fake.features.PublicLink = func(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
		return "", fs.ErrorPermissionDenied
	}

	d := &driver{fs: fake}
	if _, err := d.URL(context.Background(), "file.txt"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDriverURLSuccess(t *testing.T) {
	fake := newFakeFs()
	fake.features.PublicLink = func(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
		return "https://example.com/file.txt", nil
	}

	d := &driver{fs: fake}
	url, err := d.URL(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if url == "" {
		t.Fatalf("expected non-empty URL")
	}
}

func TestDriverURLInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.URL(context.Background(), "../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestWrapErrorPassthrough(t *testing.T) {
	sentinel := errors.New("other")
	if err := wrapError(sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("expected passthrough error, got %v", err)
	}
}

func TestDriverGetOpenError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			openErr:      errors.New("open failed"),
		}, nil
	}

	d := &driver{fs: fake}
	if _, err := d.Get(context.Background(), "file.txt"); err == nil {
		t.Fatalf("expected open error")
	}
}

func TestDriverGetReadError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			openRC:       errReadCloser{err: errors.New("read failed")},
		}, nil
	}

	d := &driver{fs: fake}
	if _, err := d.Get(context.Background(), "file.txt"); err == nil {
		t.Fatalf("expected read error")
	}
}

func TestDriverGetSuccess(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			openRC:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}

	d := &driver{fs: fake}
	data, err := d.Get(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("expected ok, got %q", data)
	}
}

func TestDriverPutMkdirError(t *testing.T) {
	fake := newFakeFs()
	fake.mkdirFunc = func(ctx context.Context, dir string) error {
		return fs.ErrorPermissionDenied
	}

	d := &driver{fs: fake}
	if err := d.Put(context.Background(), "dir/file.txt", []byte("x")); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDriverPutError(t *testing.T) {
	fake := newFakeFs()
	fake.putFunc = func(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
		return nil, hash.ErrUnsupported
	}

	d := &driver{fs: fake}
	if err := d.Put(context.Background(), "file.txt", []byte("x")); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestDriverDeleteObjectNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if err := d.Delete(context.Background(), "file.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDriverDeleteRemoveError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			removeErr:    fs.ErrorPermissionDenied,
		}, nil
	}

	d := &driver{fs: fake}
	if err := d.Delete(context.Background(), "file.txt"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDriverDeleteSuccess(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
		}, nil
	}

	d := &driver{fs: fake}
	if err := d.Delete(context.Background(), "file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDriverDeleteInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if err := d.Delete(context.Background(), "../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestDriverExistsNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	exists, err := d.Exists(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatalf("expected Exists to return false")
	}
}

func TestDriverExistsInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.Exists(context.Background(), "../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestDriverExistsTrue(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
		}, nil
	}

	d := &driver{fs: fake}
	exists, err := d.Exists(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatalf("expected Exists to return true")
	}
}

func TestDriverExistsError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorPermissionDenied
	}

	d := &driver{fs: fake}
	if _, err := d.Exists(context.Background(), "file.txt"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDriverListError(t *testing.T) {
	fake := newFakeFs()
	fake.listErr = fs.ErrorDirNotFound

	d := &driver{fs: fake}
	if _, err := d.List(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDriverListEntries(t *testing.T) {
	fake := newFakeFs()
	fake.listEntries = fs.DirEntries{
		&fakeObject{
			fakeDirEntry: fakeDirEntry{remote: "file.txt", fsys: fake, size: 12},
			openRC:       io.NopCloser(strings.NewReader("x")),
		},
		&fakeDirectory{
			fakeDirEntry: fakeDirEntry{remote: "dir", fsys: fake},
		},
	}

	d := &driver{fs: fake}
	entries, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestDriverModTimeError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if _, err := d.ModTime(context.Background(), "missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDriverModTimeInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.ModTime(context.Background(), "../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestDriverModTimeSuccess(t *testing.T) {
	fake := newFakeFs()
	want := time.Now().UTC().Truncate(time.Second)
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake, modTime: want},
		}, nil
	}

	d := &driver{fs: fake}
	got, err := d.ModTime(context.Background(), "file.txt")
	if err != nil {
		t.Fatalf("ModTime: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func resetRcloneInit(t *testing.T) {
	t.Helper()
	initOnce = sync.Once{}
	initErr = nil
	initConfigPath = ""
	initConfigData = ""
	_ = config.SetConfigPath("")
	setConfigPath = config.SetConfigPath
	installConfig = configfile.Install
}

type fakeFs struct {
	listEntries   fs.DirEntries
	listErr       error
	features      *fs.Features
	newObjectFunc func(ctx context.Context, remote string) (fs.Object, error)
	putFunc       func(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error)
	mkdirFunc     func(ctx context.Context, dir string) error
}

func newFakeFs() *fakeFs {
	return &fakeFs{features: &fs.Features{}}
}

func (f *fakeFs) Name() string {
	return "fake"
}

func (f *fakeFs) Root() string {
	return ""
}

func (f *fakeFs) String() string {
	return "fake"
}

func (f *fakeFs) Precision() time.Duration {
	return time.Nanosecond
}

func (f *fakeFs) Hashes() hash.Set {
	return hash.NewHashSet()
}

func (f *fakeFs) Features() *fs.Features {
	return f.features
}

func (f *fakeFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	return f.listEntries, f.listErr
}

func (f *fakeFs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	if f.newObjectFunc != nil {
		return f.newObjectFunc(ctx, remote)
	}
	return nil, fs.ErrorObjectNotFound
}

func (f *fakeFs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	if f.putFunc != nil {
		return f.putFunc(ctx, in, src, options...)
	}
	return nil, errors.New("not implemented")
}

func (f *fakeFs) Mkdir(ctx context.Context, dir string) error {
	if f.mkdirFunc != nil {
		return f.mkdirFunc(ctx, dir)
	}
	return nil
}

func (f *fakeFs) Rmdir(ctx context.Context, dir string) error {
	return nil
}

type fakeDirEntry struct {
	remote  string
	fsys    fs.Info
	size    int64
	modTime time.Time
}

func (d *fakeDirEntry) Fs() fs.Info {
	return d.fsys
}

func (d *fakeDirEntry) String() string {
	return d.remote
}

func (d *fakeDirEntry) Remote() string {
	return d.remote
}

func (d *fakeDirEntry) ModTime(ctx context.Context) time.Time {
	if d.modTime.IsZero() {
		return time.Time{}
	}
	return d.modTime
}

func (d *fakeDirEntry) Size() int64 {
	return d.size
}

type fakeDirectory struct {
	fakeDirEntry
}

func (d *fakeDirectory) Items() int64 {
	return -1
}

func (d *fakeDirectory) ID() string {
	return ""
}

type fakeObject struct {
	fakeDirEntry
	openRC    io.ReadCloser
	openErr   error
	removeErr error
}

func (o *fakeObject) Hash(ctx context.Context, ty hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}

func (o *fakeObject) Storable() bool {
	return true
}

func (o *fakeObject) SetModTime(ctx context.Context, t time.Time) error {
	return nil
}

func (o *fakeObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if o.openErr != nil {
		return nil, o.openErr
	}
	if o.openRC != nil {
		return o.openRC, nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (o *fakeObject) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	return nil
}

func (o *fakeObject) Remove(ctx context.Context) error {
	if o.removeErr != nil {
		return o.removeErr
	}
	return nil
}

type errReadCloser struct {
	err error
}

func (e errReadCloser) Read(_ []byte) (int, error) {
	return 0, e.err
}

func (e errReadCloser) Close() error {
	return nil
}

func TestInitRcloneInvalidConfigData(t *testing.T) {
	resetRcloneInit(t)

	err := initRclone(storage.ResolvedConfig{RcloneConfigData: "bad line"})
	if err == nil {
		t.Fatalf("expected error for invalid config data")
	}
}

func TestDriverStripPrefixEmpty(t *testing.T) {
	d := &driver{prefix: ""}
	if got := d.stripPrefix("file.txt"); got != "file.txt" {
		t.Fatalf("stripPrefix returned %q", got)
	}
}

func TestInitRclonePathAlreadySetNoConflict(t *testing.T) {
	resetRcloneInit(t)

	path1 := filepath.Join(t.TempDir(), "rclone.conf")
	if err := initRclone(storage.ResolvedConfig{RcloneConfigPath: path1}); err != nil {
		t.Fatalf("initRclone path1: %v", err)
	}
	if err := initRclone(storage.ResolvedConfig{RcloneConfigPath: path1}); err != nil {
		t.Fatalf("expected no error for same config path, got %v", err)
	}
}

func TestNewInvalidPrefix(t *testing.T) {
	_, err := New(
		context.Background(),
		Config{Remote: "localdisk:", Prefix: "../escape"},
	)
	if err == nil {
		t.Fatalf("expected error for invalid prefix")
	}
}

func TestInitRcloneConfigDataSetsConfigPath(t *testing.T) {
	resetRcloneInit(t)

	conf, err := RenderLocal(LocalRemote{Name: "localdisk"})
	if err != nil {
		t.Fatalf("RenderLocal: %v", err)
	}

	if err := initRclone(storage.ResolvedConfig{RcloneConfigData: conf}); err != nil {
		t.Fatalf("initRclone: %v", err)
	}

	if !strings.HasSuffix(config.GetConfigPath(), "inline-rclone.conf") {
		t.Fatalf("expected config path to end with inline-rclone.conf, got %q", config.GetConfigPath())
	}
}
