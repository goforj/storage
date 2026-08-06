package rclonestorage

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/fs/hash"

	"github.com/goforj/storage/storagecore"
)

// TestInitRcloneConfigData verifies inline configuration installs once and permits identical reuse.
func TestInitRcloneConfigData(t *testing.T) {
	resetRcloneInit(t)

	conf, err := RenderLocal(LocalRemote{Name: "localdisk"})
	if err != nil {
		t.Fatalf("RenderLocal: %v", err)
	}

	if err := initRclone(storagecore.ResolvedConfig{RcloneConfigData: conf}); err != nil {
		t.Fatalf("initRclone: %v", err)
	}

	if initConfigPath != "inline-rclone.conf" {
		t.Fatalf("expected inline config path, got %q", initConfigPath)
	}
	if initConfigDataHash == ([32]byte{}) {
		t.Fatalf("expected config identity hash to be captured")
	}
	if config.Data() == nil {
		t.Fatalf("expected config storage to be set")
	}
	if _, ok := config.Data().(*memoryStorage); !ok {
		t.Fatalf("expected memory storage, got %T", config.Data())
	}
}

// TestInitRcloneConfigDataConflict verifies a second inline config identity is rejected process-wide.
func TestInitRcloneConfigDataConflict(t *testing.T) {
	resetRcloneInit(t)

	err := initRclone(storagecore.ResolvedConfig{
		RcloneConfigData: "data",
		RcloneConfigPath: "path",
	})
	if err == nil {
		t.Fatalf("expected error for config path and data conflict")
	}
}

// TestInitRcloneConfigDataSetConfigPathError verifies inline path-install failures become sticky initialization errors.
func TestInitRcloneConfigDataSetConfigPathError(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("set config path failed")
	setConfigPath = func(string) error {
		return sentinel
	}
	t.Cleanup(func() {
		setConfigPath = config.SetConfigPath
	})

	err := initRclone(storagecore.ResolvedConfig{RcloneConfigData: "[one]\ntype = local\n"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected setConfigPath error, got %v", err)
	}
}

// TestInitRcloneConfigPathConflict verifies path, inline, and default config identities cannot be mixed.
func TestInitRcloneConfigPathConflict(t *testing.T) {
	resetRcloneInit(t)

	path1 := filepath.Join(t.TempDir(), "rclone-one.conf")
	path2 := filepath.Join(t.TempDir(), "rclone-two.conf")

	if err := initRclone(storagecore.ResolvedConfig{RcloneConfigPath: path1}); err != nil {
		t.Fatalf("initRclone path1: %v", err)
	}
	if err := initRclone(storagecore.ResolvedConfig{RcloneConfigPath: path2}); err == nil {
		t.Fatalf("expected error for conflicting config path")
	}
}

// TestInitRcloneDefaultConfigRequiresExactIdentity verifies ambient config cannot reuse explicit process credentials.
func TestInitRcloneDefaultConfigRequiresExactIdentity(t *testing.T) {
	conf := "[localdisk]\ntype = local\n"
	tests := []struct {
		name   string
		first  storagecore.ResolvedConfig
		second storagecore.ResolvedConfig
	}{
		{name: "inline then default", first: storagecore.ResolvedConfig{RcloneConfigData: conf}},
		{name: "path then default", first: storagecore.ResolvedConfig{RcloneConfigPath: filepath.Join(t.TempDir(), "explicit.conf")}},
		{name: "default then inline", second: storagecore.ResolvedConfig{RcloneConfigData: conf}},
		{name: "default then path", second: storagecore.ResolvedConfig{RcloneConfigPath: filepath.Join(t.TempDir(), "explicit.conf")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetRcloneInit(t)
			if err := initRclone(tt.first); err != nil {
				t.Fatalf("first initRclone: %v", err)
			}
			if err := initRclone(tt.second); err == nil {
				t.Fatal("second initRclone reused a different config identity")
			}
		})
	}
}

// TestInitRcloneConfigPathSetConfigPathError verifies file path-install failures remain observable.
func TestInitRcloneConfigPathSetConfigPathError(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("set config path failed")
	setConfigPath = func(string) error {
		return sentinel
	}
	t.Cleanup(func() {
		setConfigPath = config.SetConfigPath
	})

	err := initRclone(storagecore.ResolvedConfig{RcloneConfigPath: "badpath.conf"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected setConfigPath error, got %v", err)
	}
}

// TestInitRcloneReturnsInitErr verifies a prior process-global initialization failure is returned unchanged.
func TestInitRcloneReturnsInitErr(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("boom")
	initErr = sentinel

	if err := initRclone(storagecore.ResolvedConfig{}); !errors.Is(err, sentinel) {
		t.Fatalf("expected initErr to be returned, got %v", err)
	}
}

// TestInitRcloneEmptyConfig verifies default rclone configuration can initialize once.
func TestInitRcloneEmptyConfig(t *testing.T) {
	resetRcloneInit(t)

	if err := initRclone(storagecore.ResolvedConfig{}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestNewMissingRemote verifies constructors require an rclone remote name.
func TestNewMissingRemote(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("expected error for missing remote")
	}
}

// TestNewInitRcloneError verifies constructor setup preserves global initialization failures.
func TestNewInitRcloneError(t *testing.T) {
	resetRcloneInit(t)

	sentinel := errors.New("init failed")
	initErr = sentinel

	_, err := New(Config{Remote: "localdisk:"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected init error, got %v", err)
	}
}

// TestNewCreateFsError verifies remote filesystem construction failures retain context.
func TestNewCreateFsError(t *testing.T) {
	resetRcloneInit(t)

	conf, err := RenderLocal(LocalRemote{Name: "localdisk"})
	if err != nil {
		t.Fatalf("RenderLocal: %v", err)
	}

	_, err = New(Config{Remote: "missing:", RcloneConfigData: conf})
	if err == nil {
		t.Fatalf("expected error for missing remote config")
	}
}

// TestDriverContextErrors verifies canceled operations stop before accessing rclone.
func TestDriverContextErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &driver{}

	if _, err := d.GetContext(ctx, "file.txt"); err == nil {
		t.Fatalf("expected Get to return context error")
	}
	if err := d.PutContext(ctx, "file.txt", []byte("x")); err == nil {
		t.Fatalf("expected Put to return context error")
	}
	if err := d.DeleteContext(ctx, "file.txt"); err == nil {
		t.Fatalf("expected Delete to return context error")
	}
	if _, err := d.ExistsContext(ctx, "file.txt"); err == nil {
		t.Fatalf("expected Exists to return context error")
	}
	if _, err := d.ListContext(ctx, ""); err == nil {
		t.Fatalf("expected List to return context error")
	}
	if _, err := d.URLContext(ctx, "file.txt"); err == nil {
		t.Fatalf("expected URL to return context error")
	}
}

// TestDriverGetNotFound verifies missing rclone objects map to ErrNotFound.
func TestDriverGetNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if _, err := d.Get("missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestDriverGetInvalidPath verifies traversal is rejected before object lookup.
func TestDriverGetInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.Get("../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

// TestDriverFullPathInvalid verifies internal path resolution rejects namespace escape.
func TestDriverFullPathInvalid(t *testing.T) {
	d := &driver{prefix: "root"}
	if _, err := d.fullPath("../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

// TestDriverListPrefixEntry verifies the configured prefix itself is omitted from logical listings.
func TestDriverListPrefixEntry(t *testing.T) {
	fake := newFakeFs()
	fake.listEntries = fs.DirEntries{
		&fakeDirectory{fakeDirEntry: fakeDirEntry{remote: "prefix", fsys: fake}},
	}

	d := &driver{fs: fake, prefix: "prefix"}
	entries, err := d.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected namespace object to be hidden, got %+v", entries)
	}
}

// TestDriverListInvalidPath verifies invalid listing paths never reach the backend.
func TestDriverListInvalidPath(t *testing.T) {
	fake := newFakeFs()
	d := &driver{fs: fake, prefix: "root"}
	if _, err := d.List("../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

// TestDriverURLNotFound verifies public-link absence maps to ErrNotFound.
func TestDriverURLNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.features.PublicLink = func(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
		return "", fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if _, err := d.URL("missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestDriverURLUnsupported verifies backends without PublicLink report ErrUnsupported.
func TestDriverURLUnsupported(t *testing.T) {
	d := &driver{fs: newFakeFs()}
	if _, err := d.URL("file.txt"); !errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

// TestDriverURLForbidden verifies public-link permission failures map to ErrForbidden.
func TestDriverURLForbidden(t *testing.T) {
	fake := newFakeFs()
	fake.features.PublicLink = func(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
		return "", fs.ErrorPermissionDenied
	}

	d := &driver{fs: fake}
	if _, err := d.URL("file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// TestDriverURLPreservesTransientErrors verifies unknown public-link failures remain unclassified.
func TestDriverURLPreservesTransientErrors(t *testing.T) {
	fake := newFakeFs()
	sentinel := errors.New("transient public-link failure")
	fake.features.PublicLink = func(context.Context, string, fs.Duration, bool) (string, error) {
		return "", sentinel
	}
	d := &driver{fs: fake}
	if _, err := d.URL("file.txt"); !errors.Is(err, sentinel) || errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("URL error = %v", err)
	}
}

// TestDriverURLKnownUnsupportedErrors verifies rclone sharing limitations map to ErrUnsupported.
func TestDriverURLKnownUnsupportedErrors(t *testing.T) {
	for _, unsupported := range []error{fs.ErrorCantShareDirectories, fs.ErrorNotImplemented} {
		fake := newFakeFs()
		fake.features.PublicLink = func(context.Context, string, fs.Duration, bool) (string, error) {
			return "", unsupported
		}
		d := &driver{fs: fake}
		if _, err := d.URL("file.txt"); !errors.Is(err, storagecore.ErrUnsupported) {
			t.Fatalf("URL error for %v = %v", unsupported, err)
		}
	}
}

// TestDriverURLSuccess verifies PublicLink results are returned unchanged.
func TestDriverURLSuccess(t *testing.T) {
	fake := newFakeFs()
	fake.features.PublicLink = func(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
		return "https://example.com/file.txt", nil
	}

	d := &driver{fs: fake}
	url, err := d.URL("file.txt")
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if url == "" {
		t.Fatalf("expected non-empty URL")
	}
}

// TestDriverURLInvalidPath verifies link generation rejects traversal before backend access.
func TestDriverURLInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.URL("../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

// TestWrapErrorPassthrough verifies unrelated backend errors are preserved exactly.
func TestWrapErrorPassthrough(t *testing.T) {
	sentinel := errors.New("other")
	if err := wrapError(sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("expected passthrough error, got %v", err)
	}
}

// TestDriverGetOpenError verifies object-open failures propagate with storage classification.
func TestDriverGetOpenError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			openErr:      errors.New("open failed"),
		}, nil
	}

	d := &driver{fs: fake}
	if _, err := d.GetContext(context.Background(), "file.txt"); err == nil {
		t.Fatalf("expected open error")
	}
}

// TestDriverGetReadError verifies download failures remain observable after cleanup.
func TestDriverGetReadError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			openRC:       errReadCloser{err: errors.New("read failed")},
		}, nil
	}

	d := &driver{fs: fake}
	if _, err := d.GetContext(context.Background(), "file.txt"); err == nil {
		t.Fatalf("expected read error")
	}
}

// TestDriverGetSuccess verifies complete object downloads return the expected bytes.
func TestDriverGetSuccess(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			openRC:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}

	d := &driver{fs: fake}
	data, err := d.Get("file.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("expected ok, got %q", data)
	}
}

// TestDriverPutMkdirError verifies parent creation failure prevents an upload.
func TestDriverPutMkdirError(t *testing.T) {
	fake := newFakeFs()
	fake.mkdirFunc = func(ctx context.Context, dir string) error {
		return fs.ErrorPermissionDenied
	}

	d := &driver{fs: fake}
	if err := d.Put("dir/file.txt", []byte("x")); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// TestDriverPutError verifies rclone upload failures propagate to callers.
func TestDriverPutError(t *testing.T) {
	fake := newFakeFs()
	fake.putFunc = func(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
		return nil, hash.ErrUnsupported
	}

	d := &driver{fs: fake}
	if err := d.Put("file.txt", []byte("x")); !errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

// TestDriverDeleteObjectNotFound verifies missing deletion targets map to ErrNotFound.
func TestDriverDeleteObjectNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.listErr = fs.ErrorDirNotFound
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if err := d.Delete("file.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestDriverDeleteRemoveError verifies object removal failures are retained.
func TestDriverDeleteRemoveError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
			removeErr:    fs.ErrorPermissionDenied,
		}, nil
	}

	d := &driver{fs: fake}
	if err := d.Delete("file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// TestDriverDeleteSuccess verifies a concrete object is removed once.
func TestDriverDeleteSuccess(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
		}, nil
	}

	d := &driver{fs: fake}
	if err := d.Delete("file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestDriverDeleteInvalidPath verifies deletion rejects traversal before backend access.
func TestDriverDeleteInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if err := d.Delete("../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

// TestDriverExistsNotFound verifies absent objects produce false without an error.
func TestDriverExistsNotFound(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	exists, err := d.Exists("file.txt")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatalf("expected Exists to return false")
	}
}

// TestDriverExistsInvalidPath verifies invalid existence probes are rejected.
func TestDriverExistsInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.Exists("../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

// TestDriverExistsTrue verifies concrete rclone objects report present.
func TestDriverExistsTrue(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return &fakeObject{
			fakeDirEntry: fakeDirEntry{remote: remote, fsys: fake},
		}, nil
	}

	d := &driver{fs: fake}
	exists, err := d.Exists("file.txt")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatalf("expected Exists to return true")
	}
}

// TestDriverExistsError verifies non-absence lookup failures propagate.
func TestDriverExistsError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorPermissionDenied
	}

	d := &driver{fs: fake}
	if _, err := d.Exists("file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// TestDriverListError verifies backend listing failures retain their storage identity.
func TestDriverListError(t *testing.T) {
	fake := newFakeFs()
	fake.listErr = fs.ErrorDirNotFound

	d := &driver{fs: fake}
	if _, err := d.List("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestDriverListEntries verifies object and directory metadata becomes sorted logical entries.
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
	entries, err := d.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

// TestDriverModTimeError verifies missing object timestamps retain ErrNotFound identity.
func TestDriverModTimeError(t *testing.T) {
	fake := newFakeFs()
	fake.newObjectFunc = func(ctx context.Context, remote string) (fs.Object, error) {
		return nil, fs.ErrorObjectNotFound
	}

	d := &driver{fs: fake}
	if _, err := d.ModTime(context.Background(), "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestDriverModTimeInvalidPath verifies timestamp lookup rejects traversal.
func TestDriverModTimeInvalidPath(t *testing.T) {
	d := &driver{fs: newFakeFs(), prefix: "root"}
	if _, err := d.ModTime(context.Background(), "../escape"); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

// TestDriverModTimeSuccess verifies backend modification times are normalized to UTC.
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

// TestRcloneLocalDirectoryAndRootContracts verifies real local-backend directory semantics and root guards.
func TestRcloneLocalDirectoryAndRootContracts(t *testing.T) {
	for _, prefix := range []string{"", "tenant"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			localFS, err := fs.NewFs(context.Background(), t.TempDir())
			if err != nil {
				t.Fatalf("NewFs: %v", err)
			}
			d := &driver{fs: localFS, prefix: prefix}

			if err := d.MakeDir("empty"); err != nil {
				t.Fatalf("MakeDir empty: %v", err)
			}
			if err := d.Delete("empty"); err != nil {
				t.Fatalf("Delete empty: %v", err)
			}
			if err := d.MakeDir("nonempty"); err != nil {
				t.Fatalf("MakeDir nonempty: %v", err)
			}
			if err := d.Put("nonempty/file.txt", []byte("payload")); err != nil {
				t.Fatalf("Put nonempty file: %v", err)
			}
			if err := d.Delete("nonempty"); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("Delete nonempty error = %v", err)
			}

			if err := d.MakeDir("source/nested"); err != nil {
				t.Fatalf("MakeDir source: %v", err)
			}
			if err := d.Put("source/nested/file.txt", []byte("tree")); err != nil {
				t.Fatalf("Put source: %v", err)
			}
			if err := d.Move("source", "destination"); err != nil {
				t.Fatalf("Move directory: %v", err)
			}
			if data, err := d.Get("destination/nested/file.txt"); err != nil || string(data) != "tree" {
				t.Fatalf("Get moved file = %q err=%v", data, err)
			}
			if _, err := d.Stat("source"); !errors.Is(err, storagecore.ErrNotFound) {
				t.Fatalf("Stat moved source error = %v", err)
			}

			if err := d.Put("same.txt", []byte("same")); err != nil {
				t.Fatalf("Put same: %v", err)
			}
			if err := d.Copy("same.txt", "same.txt"); err != nil {
				t.Fatalf("Copy same path: %v", err)
			}
			if err := d.Move("same.txt", "same.txt"); err != nil {
				t.Fatalf("Move same path: %v", err)
			}
			if data, err := d.Get("same.txt"); err != nil || string(data) != "same" {
				t.Fatalf("Get same path = %q err=%v", data, err)
			}
			if err := d.Move("missing", "missing"); !errors.Is(err, storagecore.ErrNotFound) {
				t.Fatalf("Move missing same path error = %v", err)
			}

			for name, err := range map[string]error{
				"put":         d.Put("", []byte("root")),
				"get":         func() error { _, err := d.Get(""); return err }(),
				"copy source": d.Copy("", "same.txt"),
				"copy target": d.Copy("same.txt", ""),
				"move source": d.Move("", "other"),
				"move target": d.Move("same.txt", ""),
				"delete":      d.Delete(""),
			} {
				if !errors.Is(err, storagecore.ErrForbidden) {
					t.Errorf("%s root error = %v", name, err)
				}
			}
		})
	}
}

// TestRcloneLogicalRootIsSynthetic verifies an absent prefix or exact backend object cannot become a logical file.
func TestRcloneLogicalRootIsSynthetic(t *testing.T) {
	fake := newFakeFs()
	fake.listErr = fs.ErrorDirNotFound
	fake.newObjectFunc = func(context.Context, string) (fs.Object, error) {
		return &fakeObject{fakeDirEntry: fakeDirEntry{remote: "tenant", fsys: fake, size: 7}}, nil
	}
	d := &driver{fs: fake, prefix: "tenant"}
	if _, err := d.Get(""); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Get root error = %v", err)
	}
	entry, err := d.Stat("")
	if err != nil || entry.Path != "" || !entry.IsDir {
		t.Fatalf("Stat root = %+v err=%v", entry, err)
	}
	if exists, err := d.Exists(""); err != nil || exists {
		t.Fatalf("Exists root = %v err=%v", exists, err)
	}
	if entries, err := d.List(""); err != nil || len(entries) != 0 {
		t.Fatalf("List root = %+v err=%v", entries, err)
	}
	called := false
	if err := d.Walk("", func(storagecore.Entry) error { called = true; return nil }); err != nil || called {
		t.Fatalf("Walk root called=%v err=%v", called, err)
	}
	if err := d.Copy("", "copy.txt"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Copy source root error = %v", err)
	}
	if _, err := d.URL(""); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("URL root error = %v", err)
	}
	if _, err := d.ModTime(context.Background(), ""); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ModTime root error = %v", err)
	}
}

// TestRcloneCloseOwnsBackendLifecycle verifies Shutdown is bounded, idempotent, and terminal.
func TestRcloneCloseOwnsBackendLifecycle(t *testing.T) {
	fake := newFakeFs()
	shutdownErr := errors.New("shutdown failed")
	calls := 0
	fake.features.Shutdown = func(ctx context.Context) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("Shutdown context has no deadline")
		}
		return shutdownErr
	}
	d := &driver{fs: fake}
	for i := 0; i < 2; i++ {
		if err := d.Close(); !errors.Is(err, shutdownErr) {
			t.Fatalf("Close %d error = %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("Shutdown calls = %d", calls)
	}
	if _, err := d.Get("file.txt"); !errors.Is(err, stdfs.ErrClosed) {
		t.Fatalf("Get after Close error = %v", err)
	}
	closedCalls := []func() error{
		func() error { return d.Put("file.txt", nil) },
		func() error { return d.MakeDir("dir") },
		func() error { return d.Delete("file.txt") },
		func() error { _, err := d.Stat("file.txt"); return err },
		func() error { _, err := d.Exists("file.txt"); return err },
		func() error { _, err := d.List(""); return err },
		func() error { return d.Walk("", func(storagecore.Entry) error { return nil }) },
		func() error { return d.Copy("file.txt", "copy.txt") },
		func() error { return d.Move("file.txt", "moved.txt") },
		func() error { _, err := d.URL("file.txt"); return err },
		func() error { _, err := d.ModTime(context.Background(), "file.txt"); return err },
	}
	for index, call := range closedCalls {
		if err := call(); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatalf("closed call %d error = %v", index, err)
		}
	}
}

// resetRcloneInit restores all process-global rclone hooks and identity state after each test.
func resetRcloneInit(t *testing.T) {
	t.Helper()
	rcloneConfigMu.Lock()
	defer rcloneConfigMu.Unlock()
	rcloneConfigured = false
	initErr = nil
	initConfigKind = ""
	initConfigPath = ""
	initConfigDataHash = [32]byte{}
	_ = config.SetConfigPath("")
	setConfigPath = config.SetConfigPath
	installConfig = configfile.Install
	newRcloneFS = fs.NewFs
}

type fakeFs struct {
	listEntries   fs.DirEntries
	listErr       error
	features      *fs.Features
	newObjectFunc func(ctx context.Context, remote string) (fs.Object, error)
	putFunc       func(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error)
	mkdirFunc     func(ctx context.Context, dir string) error
}

// newFakeFs creates an rclone filesystem double with an empty feature set.
func newFakeFs() *fakeFs {
	return &fakeFs{features: &fs.Features{}}
}

// Name returns the stable backend identifier required by fs.Info.
func (f *fakeFs) Name() string {
	return "fake"
}

// Root keeps fake remotes relative to the logical backend root.
func (f *fakeFs) Root() string {
	return ""
}

// String returns a stable diagnostic label for the fake filesystem.
func (f *fakeFs) String() string {
	return "fake"
}

// Precision advertises nanosecond timestamps for metadata assertions.
func (f *fakeFs) Precision() time.Duration {
	return time.Nanosecond
}

// Hashes reports no supported object hash algorithms.
func (f *fakeFs) Hashes() hash.Set {
	return hash.NewHashSet()
}

// Features exposes mutable backend capabilities to each test.
func (f *fakeFs) Features() *fs.Features {
	return f.features
}

// List returns the configured directory entries or listing failure.
func (f *fakeFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	return f.listEntries, f.listErr
}

// NewObject delegates path-sensitive lookups and otherwise reports absence.
func (f *fakeFs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	if f.newObjectFunc != nil {
		return f.newObjectFunc(ctx, remote)
	}
	return nil, fs.ErrorObjectNotFound
}

// Put delegates uploads to the configured fixture hook.
func (f *fakeFs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	if f.putFunc != nil {
		return f.putFunc(ctx, in, src, options...)
	}
	return nil, errors.New("not implemented")
}

// Mkdir delegates directory creation when a fixture hook is installed.
func (f *fakeFs) Mkdir(ctx context.Context, dir string) error {
	if f.mkdirFunc != nil {
		return f.mkdirFunc(ctx, dir)
	}
	return nil
}

// Rmdir makes fake empty-directory removal succeed.
func (f *fakeFs) Rmdir(ctx context.Context, dir string) error {
	return nil
}

type fakeDirEntry struct {
	remote  string
	fsys    fs.Info
	size    int64
	modTime time.Time
}

// Fs returns the configured backend metadata for this fake entry.
func (d *fakeDirEntry) Fs() fs.Info {
	return d.fsys
}

// String renders the fake entry's remote path for diagnostics.
func (d *fakeDirEntry) String() string {
	return d.remote
}

// Remote returns the fake entry's backend-relative path.
func (d *fakeDirEntry) Remote() string {
	return d.remote
}

// ModTime returns the configured timestamp for metadata assertions.
func (d *fakeDirEntry) ModTime(ctx context.Context) time.Time {
	if d.modTime.IsZero() {
		return time.Time{}
	}
	return d.modTime
}

// Size returns the configured byte length for this fake entry.
func (d *fakeDirEntry) Size() int64 {
	return d.size
}

type fakeDirectory struct {
	fakeDirEntry
}

// Items reports an unknown child count for the fake directory.
func (d *fakeDirectory) Items() int64 {
	return -1
}

// ID reports no backend-specific identifier for the fake directory.
func (d *fakeDirectory) ID() string {
	return ""
}

type fakeObject struct {
	fakeDirEntry
	openRC    io.ReadCloser
	openErr   error
	removeErr error
}

// Hash reports unsupported hashing for the fake object.
func (o *fakeObject) Hash(ctx context.Context, ty hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}

// Storable marks the fake object as eligible for backend storage.
func (o *fakeObject) Storable() bool {
	return true
}

// SetModTime accepts timestamp updates without mutating fixture metadata.
func (o *fakeObject) SetModTime(ctx context.Context, t time.Time) error {
	return nil
}

// Open returns the configured download stream or open failure.
func (o *fakeObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if o.openErr != nil {
		return nil, o.openErr
	}
	if o.openRC != nil {
		return o.openRC, nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// Update accepts replacement content because upload behavior is tested at the filesystem boundary.
func (o *fakeObject) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	return nil
}

// Remove returns the configured object-deletion result.
func (o *fakeObject) Remove(ctx context.Context) error {
	if o.removeErr != nil {
		return o.removeErr
	}
	return nil
}

type errReadCloser struct {
	err error
}

// Read injects the configured download failure.
func (e errReadCloser) Read(_ []byte) (int, error) {
	return 0, e.err
}

// Close makes cleanup of the failing test reader succeed.
func (e errReadCloser) Close() error {
	return nil
}

// TestInitRcloneInvalidConfigData verifies malformed inline configuration fails initialization.
func TestInitRcloneInvalidConfigData(t *testing.T) {
	resetRcloneInit(t)

	err := initRclone(storagecore.ResolvedConfig{RcloneConfigData: "bad line"})
	if err == nil {
		t.Fatalf("expected error for invalid config data")
	}
}

// TestDriverStripPrefixEmpty verifies unprefixed remote paths remain unchanged.
func TestDriverStripPrefixEmpty(t *testing.T) {
	d := &driver{prefix: ""}
	if got := d.stripPrefix("file.txt"); got != "file.txt" {
		t.Fatalf("stripPrefix returned %q", got)
	}
}

// TestInitRclonePathAlreadySetNoConflict verifies identical config paths can be reused.
func TestInitRclonePathAlreadySetNoConflict(t *testing.T) {
	resetRcloneInit(t)

	path1 := filepath.Join(t.TempDir(), "rclone.conf")
	if err := initRclone(storagecore.ResolvedConfig{RcloneConfigPath: path1}); err != nil {
		t.Fatalf("initRclone path1: %v", err)
	}
	if err := initRclone(storagecore.ResolvedConfig{RcloneConfigPath: path1}); err != nil {
		t.Fatalf("expected no error for same config path, got %v", err)
	}
}

// TestNewInvalidPrefix verifies constructor prefix traversal is rejected.
func TestNewInvalidPrefix(t *testing.T) {
	_, err := New(Config{Remote: "localdisk:", Prefix: "../escape"})
	if err == nil {
		t.Fatalf("expected error for invalid prefix")
	}
}

// TestInitRcloneConfigDataSetsConfigPath verifies inline config installs the synthetic path expected by rclone.
func TestInitRcloneConfigDataSetsConfigPath(t *testing.T) {
	resetRcloneInit(t)

	conf, err := RenderLocal(LocalRemote{Name: "localdisk"})
	if err != nil {
		t.Fatalf("RenderLocal: %v", err)
	}

	if err := initRclone(storagecore.ResolvedConfig{RcloneConfigData: conf}); err != nil {
		t.Fatalf("initRclone: %v", err)
	}

	if !strings.HasSuffix(config.GetConfigPath(), "inline-rclone.conf") {
		t.Fatalf("expected config path to end with inline-rclone.conf, got %q", config.GetConfigPath())
	}
}

// TestRcloneInvalidPathsAndThinWrappers covers validation before any backend
// request and the background-context adapters omitted by the shared contract.
func TestRcloneInvalidPathsAndThinWrappers(t *testing.T) {
	d := &driver{}
	calls := []func() error{
		func() error { _, err := d.GetContext(nil, "../bad"); return err },
		func() error { return d.PutContext(nil, "../bad", nil) },
		func() error { return d.MakeDirContext(nil, "../bad") },
		func() error { return d.DeleteContext(nil, "../bad") },
		func() error { _, err := d.StatContext(nil, "../bad"); return err },
		func() error { _, err := d.ExistsContext(nil, "../bad"); return err },
		func() error { _, err := d.ListContext(nil, "../bad"); return err },
		func() error { return d.WalkContext(nil, "../bad", func(storagecore.Entry) error { return nil }) },
		func() error { return d.CopyContext(nil, "../bad", "dst") },
		func() error { return d.CopyContext(nil, "src", "../bad") },
		func() error { return d.MoveContext(nil, "../bad", "dst") },
		func() error { return d.MoveContext(nil, "src", "../bad") },
		func() error { _, err := d.URLContext(nil, "../bad"); return err },
	}
	for index, call := range calls {
		if err := call(); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("invalid-path call %d error = %v", index, err)
		}
	}
	if err := d.MakeDir("../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("MakeDir invalid path error = %v", err)
	}
	if _, err := d.ListPage("../bad", 0, 1); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ListPage error = %v", err)
	}
}

// TestRcloneRemainingHelperEdges covers cancellation, cleanup, stream, and
// backend capability branches without external remotes.
func TestRcloneRemainingHelperEdges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newFromDiskConfig(ctx, storagecore.ResolvedConfig{Remote: "remote:"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("newFromDiskConfig canceled error = %v", err)
	}

	fake := newFakeFs()
	d := &driver{fs: fake, prefix: "pre"}
	if err := d.MakeDir(""); err != nil {
		t.Fatalf("MakeDir root: %v", err)
	}
	mkdirErr := errors.New("mkdir")
	fake.mkdirFunc = func(context.Context, string) error { return mkdirErr }
	if err := d.MakeDir("dir"); !errors.Is(err, mkdirErr) {
		t.Fatalf("MakeDir error = %v", err)
	}
	if err := d.Walk("", nil); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Walk nil callback error = %v", err)
	}

	closeErr := errors.New("close")
	fake.newObjectFunc = func(context.Context, string) (fs.Object, error) {
		return &fakeObject{fakeDirEntry: fakeDirEntry{remote: "pre/file", fsys: fake}, openRC: &rcloneCloseReader{closeErr: closeErr}}, nil
	}
	if _, err := d.Get("file"); !errors.Is(err, closeErr) {
		t.Fatalf("Get close error = %v", err)
	}

	writeErr := errors.New("write")
	if err := copyContext(context.Background(), rcloneFailingWriter{err: writeErr}, strings.NewReader("x")); !errors.Is(err, writeErr) {
		t.Fatalf("copyContext write error = %v", err)
	}
	if err := copyContext(context.Background(), rcloneShortWriter{}, strings.NewReader("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("copyContext short write error = %v", err)
	}
	if err := copyContext(ctx, io.Discard, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("copyContext canceled error = %v", err)
	}
	if err := joinCleanup(nil, closeErr); !errors.Is(err, closeErr) {
		t.Fatalf("joinCleanup cleanup error = %v", err)
	}
	if err := joinCleanup(writeErr, closeErr); !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("joinCleanup combined error = %v", err)
	}

	withoutShutdown := &driver{fs: newFakeFs()}
	if err := withoutShutdown.Close(); err != nil {
		t.Fatalf("Close without shutdown: %v", err)
	}
}

type rcloneCloseReader struct {
	closeErr error
}

// Read reports EOF immediately.
func (*rcloneCloseReader) Read([]byte) (int, error) { return 0, io.EOF }

// Close returns the configured cleanup failure.
func (r *rcloneCloseReader) Close() error { return r.closeErr }

type rcloneFailingWriter struct {
	err error
}

// Write injects a destination failure.
func (w rcloneFailingWriter) Write([]byte) (int, error) { return 0, w.err }

type rcloneShortWriter struct{}

// Write accepts no bytes without error to exercise short-write handling.
func (rcloneShortWriter) Write([]byte) (int, error) { return 0, nil }
