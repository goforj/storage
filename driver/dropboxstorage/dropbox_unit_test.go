package dropboxstorage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/goforj/storage/storagecore"
)

// TestDropboxConstructors verifies token, prefix, context, and registry validation.
func TestDropboxConstructors(t *testing.T) {
	if got := (Config{}).DriverName(); got != "dropbox" {
		t.Fatalf("DriverName = %q", got)
	}

	t.Run("new missing token", func(t *testing.T) {
		_, err := New(Config{})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("new context success", func(t *testing.T) {
		got, err := NewContext(context.Background(), Config{Token: "token", Prefix: "pre"})
		if err != nil {
			t.Fatalf("NewContext: %v", err)
		}
		if got == nil {
			t.Fatal("NewContext returned nil storage")
		}
	})

	t.Run("invalid prefix", func(t *testing.T) {
		if _, err := New(Config{Token: "token", Prefix: "../bad"}); !errors.Is(err, storagecore.ErrForbidden) {
			t.Fatalf("New invalid prefix error = %v", err)
		}
	})

	t.Run("canceled setup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewContext(ctx, Config{Token: "token"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("NewContext canceled error = %v", err)
		}
	})
}

type errNotFound struct{}

// Error mimics Dropbox's legacy not-found summary for compatibility tests.
func (errNotFound) Error() string { return "not_found/.." }

// TestDropboxPrefixHelpers verifies API-root encoding and exact logical prefix stripping.
func TestDropboxPrefixHelpers(t *testing.T) {
	d := &driver{prefix: "pre"}
	fp, err := d.fullPath("file.txt")
	if err != nil {
		t.Fatalf("fullPath err: %v", err)
	}
	if fp != "/pre/file.txt" {
		t.Fatalf("unexpected fullPath %q", fp)
	}
	if got := d.stripPrefix("/pre/path/to"); got != "path/to" {
		t.Fatalf("stripPrefix got %q", got)
	}
	if got := (&driver{}).stripPrefix("/path/to"); got != "path/to" {
		t.Fatalf("stripPrefix without prefix got %q", got)
	}
	root, err := (&driver{}).fullPath("")
	if err != nil {
		t.Fatalf("root fullPath err: %v", err)
	}
	if root != "" {
		t.Fatalf("root fullPath = %q, want empty Dropbox root", root)
	}
}

// TestDropboxWrapError verifies Dropbox absence summaries retain ErrNotFound identity.
func TestDropboxWrapError(t *testing.T) {
	if err := wrapError(errNotFound{}); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound")
	}
	if err := wrapError(errors.New("other")); errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("unexpected ErrNotFound")
	}
}

type fakeDropbox struct {
	getData       string
	getReader     io.ReadCloser
	getErr        error
	downloadHook  func()
	putErr        error
	uploadHook    func()
	createErr     error
	delErr        error
	deleteHook    func(*files.DeleteArg) error
	moveErr       error
	metaErr       error
	metaOut       files.IsMetadata
	listErr       error
	listOut       *files.ListFolderResult
	linkErr       error
	linkURL       string
	continueOut   *files.ListFolderResult
	continueSeq   []*files.ListFolderResult
	continueArgs  []*files.ListFolderContinueArg
	uploaded      []byte
	downloadArgs  []*files.DownloadArg
	uploadArgs    []*files.UploadArg
	deleteArgs    []*files.DeleteArg
	createArgs    []*files.CreateFolderArg
	moveArgs      []*files.RelocationArg
	listArgs      []*files.ListFolderArg
	metadataArgs  []*files.GetMetadataArg
	metaByPath    map[string]files.IsMetadata
	metaErrByPath map[string]error
}

// Download records its argument and returns the configured body, payload, or failure.
func (f *fakeDropbox) Download(arg *files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error) {
	f.downloadArgs = append(f.downloadArgs, arg)
	if f.downloadHook != nil {
		f.downloadHook()
	}
	if f.getErr != nil {
		return nil, nil, f.getErr
	}
	if f.getReader != nil {
		return &files.FileMetadata{}, f.getReader, nil
	}
	return &files.FileMetadata{}, io.NopCloser(strings.NewReader(f.getData)), nil
}

// Upload captures request metadata and bytes before running any completion hook.
func (f *fakeDropbox) Upload(arg *files.UploadArg, content io.Reader) (*files.FileMetadata, error) {
	f.uploadArgs = append(f.uploadArgs, arg)
	if f.putErr != nil {
		return nil, f.putErr
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	f.uploaded = data
	if f.uploadHook != nil {
		f.uploadHook()
	}
	return &files.FileMetadata{}, nil
}

// DeleteV2 records deletion requests and delegates path-sensitive fixture behavior.
func (f *fakeDropbox) DeleteV2(arg *files.DeleteArg) (*files.DeleteResult, error) {
	f.deleteArgs = append(f.deleteArgs, arg)
	if f.deleteHook != nil {
		return nil, f.deleteHook(arg)
	}
	return nil, f.delErr
}

// CreateFolderV2 records folder creation and updates fixture metadata on success.
func (f *fakeDropbox) CreateFolderV2(arg *files.CreateFolderArg) (*files.CreateFolderResult, error) {
	f.createArgs = append(f.createArgs, arg)
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.metaByPath != nil {
		f.metaByPath[arg.Path] = &files.FolderMetadata{Metadata: files.Metadata{PathLower: arg.Path}}
	}
	return &files.CreateFolderResult{}, nil
}

// MoveV2 records relocation arguments and returns the configured failure.
func (f *fakeDropbox) MoveV2(arg *files.RelocationArg) (*files.RelocationResult, error) {
	f.moveArgs = append(f.moveArgs, arg)
	if f.moveErr != nil {
		return nil, f.moveErr
	}
	return &files.RelocationResult{}, nil
}

// GetMetadata resolves path-specific fixture metadata and failures before shared defaults.
func (f *fakeDropbox) GetMetadata(arg *files.GetMetadataArg) (files.IsMetadata, error) {
	f.metadataArgs = append(f.metadataArgs, arg)
	if err, ok := f.metaErrByPath[arg.Path]; ok {
		return nil, err
	}
	if f.metaByPath != nil {
		if metadata, ok := f.metaByPath[arg.Path]; ok {
			return metadata, nil
		}
		return nil, errNotFound{}
	}
	if f.metaErr != nil {
		return nil, f.metaErr
	}
	return f.metaOut, nil
}

// ListFolder records the initial query and returns its configured page.
func (f *fakeDropbox) ListFolder(arg *files.ListFolderArg) (*files.ListFolderResult, error) {
	f.listArgs = append(f.listArgs, arg)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.listOut != nil {
		return f.listOut, nil
	}
	return &files.ListFolderResult{}, nil
}

// ListFolderContinue records cursors and consumes configured pages iteratively.
func (f *fakeDropbox) ListFolderContinue(arg *files.ListFolderContinueArg) (*files.ListFolderResult, error) {
	f.continueArgs = append(f.continueArgs, arg)
	if len(f.continueSeq) > 0 {
		out := f.continueSeq[0]
		f.continueSeq = f.continueSeq[1:]
		return out, nil
	}
	if f.continueOut != nil {
		return f.continueOut, nil
	}
	return &files.ListFolderResult{}, nil
}

// GetTemporaryLink returns the configured public link or signing failure.
func (f *fakeDropbox) GetTemporaryLink(arg *files.GetTemporaryLinkArg) (*files.GetTemporaryLinkResult, error) {
	if f.linkErr != nil {
		return nil, f.linkErr
	}
	return &files.GetTemporaryLinkResult{Link: f.linkURL}, nil
}

// TestDropboxStorageOperations verifies core object and listing behavior through the SDK boundary.
func TestDropboxStorageOperations(t *testing.T) {
	client := &fakeDropbox{
		getData: "hello",
		metaByPath: map[string]files.IsMetadata{
			"/pre":          &files.FolderMetadata{Metadata: files.Metadata{PathLower: "/pre"}},
			"/pre/file.txt": &files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/file.txt"}, Size: 5},
		},
		listOut: &files.ListFolderResult{
			Entries: []files.IsMetadata{
				&files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/file.txt"}, Size: 3},
				&files.FolderMetadata{Metadata: files.Metadata{PathLower: "/pre/dir"}},
			},
		},
	}
	d := &driver{client: client, prefix: "pre"}

	if _, err := d.Get("file.txt"); err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if err := d.Put("file.txt", []byte("abc")); err != nil {
		t.Fatalf("Put err: %v", err)
	}
	if string(client.uploaded) != "abc" {
		t.Fatalf("uploaded = %q", client.uploaded)
	}
	exists, err := d.Exists("file.txt")
	if err != nil || !exists {
		t.Fatalf("Exists err %v exists %v", err, exists)
	}
	entry, err := d.Stat("file.txt")
	if err != nil {
		t.Fatalf("Stat err: %v", err)
	}
	if entry.Path != "file.txt" || entry.Size != 5 || entry.IsDir {
		t.Fatalf("Stat entry = %+v", entry)
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List err %v entries %v", err, entries)
	}
	if entries[0].Path != "dir" || entries[1].Path != "file.txt" {
		t.Fatalf("List order = %+v", entries)
	}
	if _, err := d.URL("file.txt"); err != nil {
		t.Fatalf("URL err: %v", err)
	}

	client.delErr = errNotFound{}
	if err := d.Delete("missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("expected wrapped not found, got %v", err)
	}
}

// TestDropboxWalk verifies recursive metadata becomes deterministic logical entries.
func TestDropboxWalk(t *testing.T) {
	client := &fakeDropbox{
		listOut: &files.ListFolderResult{
			Entries: []files.IsMetadata{
				&files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/file.txt"}, Size: 3},
			},
			HasMore: true,
			Cursor:  "cursor",
		},
		continueOut: &files.ListFolderResult{
			Entries: []files.IsMetadata{
				&files.FolderMetadata{Metadata: files.Metadata{PathLower: "/pre/dir"}},
			},
		},
	}
	d := &driver{client: client, prefix: "pre"}

	var got []storagecore.Entry
	if err := d.WalkContext(context.Background(), "", func(entry storagecore.Entry) error {
		got = append(got, entry)
		return nil
	}); err != nil {
		t.Fatalf("WalkContext: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("WalkContext entries = %v", got)
	}
	if got[0].Path != "dir" || got[1].Path != "file.txt" {
		t.Fatalf("WalkContext order = %+v", got)
	}

	want := errors.New("stop")
	if err := d.WalkContext(context.Background(), "", func(storagecore.Entry) error { return want }); !errors.Is(err, want) {
		t.Fatalf("WalkContext callback error = %v", err)
	}
}

// TestDropboxListContinue verifies continuation pages append without losing cursor state.
func TestDropboxListContinue(t *testing.T) {
	client := &fakeDropbox{
		continueOut: &files.ListFolderResult{
			Entries: []files.IsMetadata{
				&files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/file2.txt"}, Size: 4},
			},
		},
	}
	d := &driver{client: client, prefix: "pre"}

	var entries []storagecore.Entry
	if err := d.listContinue(context.Background(), files.NewListFolderContinueArg("cursor"), &entries); err != nil {
		t.Fatalf("listContinue: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "file2.txt" {
		t.Fatalf("listContinue entries = %+v", entries)
	}
}

// TestDropboxWrappersAndErrors verifies API adapters and portable error classifications.
func TestDropboxWrappersAndErrors(t *testing.T) {
	client := &fakeDropbox{
		getErr:  errors.New("boom"),
		putErr:  errors.New("boom"),
		delErr:  errors.New("boom"),
		metaErr: errors.New("boom"),
		linkErr: errors.New("boom"),
	}
	d := &driver{client: client, prefix: "pre"}

	if err := d.Walk("", func(storagecore.Entry) error { return nil }); err != nil {
		t.Fatalf("Walk wrapper: %v", err)
	}

	if _, err := d.GetContext(context.Background(), "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("GetContext invalid path error = %v", err)
	}
	if err := d.PutContext(context.Background(), "file.txt", []byte("x")); err == nil {
		t.Fatal("PutContext returned nil error")
	}
	if err := d.DeleteContext(context.Background(), "file.txt"); err == nil {
		t.Fatal("DeleteContext returned nil error")
	}
	if _, err := d.ExistsContext(context.Background(), "file.txt"); err == nil {
		t.Fatal("ExistsContext returned nil error")
	}
	if _, err := d.URLContext(context.Background(), "file.txt"); err == nil {
		t.Fatal("URLContext returned nil error")
	}
	if _, err := d.ExistsContext(context.Background(), "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ExistsContext invalid path error = %v", err)
	}
	if _, err := d.ListContext(context.Background(), "../bad"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ListContext invalid path error = %v", err)
	}
	if err := d.WalkContext(context.Background(), "../bad", func(storagecore.Entry) error { return nil }); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("WalkContext invalid path error = %v", err)
	}
}

// TestDropboxStatBranches verifies file, folder, missing, and malformed metadata handling.
func TestDropboxStatBranches(t *testing.T) {
	t.Run("folder metadata", func(t *testing.T) {
		d := &driver{
			client: &fakeDropbox{
				metaOut: &files.FolderMetadata{Metadata: files.Metadata{PathLower: "/pre/folder"}},
			},
			prefix: "pre",
		}
		entry, err := d.StatContext(context.Background(), "folder")
		if err != nil {
			t.Fatalf("StatContext: %v", err)
		}
		if !entry.IsDir || entry.Path != "folder" {
			t.Fatalf("folder entry = %+v", entry)
		}
		exists, err := d.ExistsContext(context.Background(), "folder")
		if err != nil || exists {
			t.Fatalf("ExistsContext folder = %v, %v", exists, err)
		}
	})

	t.Run("unsupported metadata", func(t *testing.T) {
		d := &driver{
			client: &fakeDropbox{
				metaOut: &files.DeletedMetadata{Metadata: files.Metadata{PathLower: "/pre/deleted"}},
			},
			prefix: "pre",
		}
		if _, err := d.StatContext(context.Background(), "deleted"); !errors.Is(err, storagecore.ErrUnsupported) {
			t.Fatalf("StatContext unsupported error = %v", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		d := &driver{client: &fakeDropbox{}, prefix: "pre"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := d.StatContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
			t.Fatalf("StatContext canceled error = %v", err)
		}
	})
}

// TestDropboxCopyAndMove verifies copy payloads and Dropbox relocation requests.
func TestDropboxCopyAndMove(t *testing.T) {
	client := &fakeDropbox{
		getData: "payload",
		metaByPath: map[string]files.IsMetadata{
			"/pre":         &files.FolderMetadata{Metadata: files.Metadata{PathLower: "/pre"}},
			"/pre/src.txt": &files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/src.txt"}},
		},
	}
	d := &driver{client: client, prefix: "pre"}

	if err := d.Copy("src.txt", "dst.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if string(client.uploaded) != "payload" {
		t.Fatalf("copied upload = %q", client.uploaded)
	}

	if err := d.Move("src.txt", "moved.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	client.moveErr = errors.New("move boom")
	if err := d.MoveContext(context.Background(), "src.txt", "broken.txt"); err == nil {
		t.Fatal("MoveContext returned nil error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.CopyContext(ctx, "src.txt", "dst.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext canceled error = %v", err)
	}
}

// TestDropboxListAndWalkErrors verifies listing, continuation, callbacks, and traversal failures propagate.
func TestDropboxListAndWalkErrors(t *testing.T) {
	client := &fakeDropbox{listErr: errors.New("boom")}
	d := &driver{client: client, prefix: "pre"}
	if _, err := d.ListContext(context.Background(), "folder"); err == nil {
		t.Fatal("ListContext returned nil error")
	}
	if err := d.walkPage(context.Background(), files.NewListFolderArg("/pre"), func(storagecore.Entry) error { return nil }); err == nil {
		t.Fatal("walkPage returned nil error")
	}

	client = &fakeDropbox{continueOut: nil, listOut: &files.ListFolderResult{HasMore: true, Cursor: "cursor"}}
	d = &driver{client: client, prefix: "pre"}
	if err := d.WalkContext(context.Background(), "", func(storagecore.Entry) error { return nil }); err != nil {
		t.Fatalf("WalkContext empty continue: %v", err)
	}

	client = &fakeDropbox{}
	d = &driver{client: client, prefix: "pre"}
	var entries []storagecore.Entry
	if err := d.listContinue(context.Background(), files.NewListFolderContinueArg("cursor"), &entries); err != nil {
		t.Fatalf("listContinue empty: %v", err)
	}

	client = &fakeDropbox{
		continueSeq: []*files.ListFolderResult{
			{
				Entries: []files.IsMetadata{
					&files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/file3.txt"}, Size: 5},
				},
				HasMore: true,
				Cursor:  "next",
			},
			{
				Entries: []files.IsMetadata{
					&files.FolderMetadata{Metadata: files.Metadata{PathLower: "/pre/folder"}},
				},
			},
		},
	}
	d = &driver{client: client, prefix: "pre"}
	entries = nil
	if err := d.listContinue(context.Background(), files.NewListFolderContinueArg("cursor"), &entries); err != nil {
		t.Fatalf("listContinue recursive: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("listContinue recursive entries = %+v", entries)
	}
}

// TestDropboxGetContextReadFailure verifies failed downloads still close their response body.
func TestDropboxGetContextReadFailure(t *testing.T) {
	body := &trackedReadCloser{reader: errReader{}}
	client := &fakeDropbox{
		getReader: body,
	}
	d := &driver{client: client, prefix: "pre"}
	if _, err := d.GetContext(context.Background(), "file.txt"); err == nil {
		t.Fatal("GetContext returned nil error")
	}
	if !body.closed {
		t.Fatal("GetContext did not close failed download body")
	}
}

// TestDropboxExistsFalseOnNotFound verifies missing metadata produces false without an error.
func TestDropboxExistsFalseOnNotFound(t *testing.T) {
	d := &driver{client: &fakeDropbox{metaErr: errNotFound{}}, prefix: "pre"}
	ok, err := d.Exists("missing.txt")
	if err != nil || ok {
		t.Fatalf("Exists missing = %v err=%v", ok, err)
	}
}

type errReader struct{}

// Read injects a deterministic failure after a download opens.
func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// trackedReadCloser records cleanup and can inject a close failure.
type trackedReadCloser struct {
	reader   io.Reader
	closeErr error
	closed   bool
}

// Read delegates stream reads to the configured reader.
func (r *trackedReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

// Close records cleanup before returning the configured failure.
func (r *trackedReadCloser) Close() error {
	r.closed = true
	return r.closeErr
}

// TestDropboxGetContextClosesDownload verifies response cleanup errors remain observable.
func TestDropboxGetContextClosesDownload(t *testing.T) {
	closeErr := errors.New("close failed")
	body := &trackedReadCloser{reader: strings.NewReader("contents"), closeErr: closeErr}
	d := &driver{client: &fakeDropbox{getReader: body}}
	if _, err := d.GetContext(context.Background(), "file.txt"); !errors.Is(err, closeErr) {
		t.Fatalf("GetContext close error = %v", err)
	}
	if !body.closed {
		t.Fatal("GetContext did not close download body")
	}
}

// TestDropboxGetContextClosesBodyAfterSDKCancellation verifies post-call cancellation cannot leak a response.
func TestDropboxGetContextClosesBodyAfterSDKCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closeErr := errors.New("close failed")
	body := &trackedReadCloser{reader: strings.NewReader("contents"), closeErr: closeErr}
	d := &driver{client: &fakeDropbox{getReader: body, downloadHook: cancel}}
	_, err := d.GetContext(ctx, "file.txt")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) {
		t.Fatalf("GetContext error = %v", err)
	}
	if !body.closed {
		t.Fatal("GetContext did not close the canceled response body")
	}
}

// TestDropboxPutOverwritesAndCreatesParents verifies parent creation and overwrite upload mode.
func TestDropboxPutOverwritesAndCreatesParents(t *testing.T) {
	client := &fakeDropbox{metaByPath: make(map[string]files.IsMetadata)}
	d := &driver{client: client, prefix: "pre"}

	if err := d.PutContext(context.Background(), "one/two/file.txt", []byte("contents")); err != nil {
		t.Fatalf("PutContext: %v", err)
	}
	wantCreated := []string{"/pre", "/pre/one", "/pre/one/two"}
	if len(client.createArgs) != len(wantCreated) {
		t.Fatalf("created paths = %v", createPaths(client.createArgs))
	}
	for i, want := range wantCreated {
		if client.createArgs[i].Path != want {
			t.Fatalf("created path %d = %q, want %q", i, client.createArgs[i].Path, want)
		}
	}
	if len(client.uploadArgs) != 1 {
		t.Fatalf("upload calls = %d", len(client.uploadArgs))
	}
	if client.uploadArgs[0].Path != "/pre/one/two/file.txt" {
		t.Fatalf("upload path = %q", client.uploadArgs[0].Path)
	}
	if client.uploadArgs[0].Mode == nil || client.uploadArgs[0].Mode.Tag != files.WriteModeOverwrite {
		t.Fatalf("upload mode = %+v, want overwrite", client.uploadArgs[0].Mode)
	}
}

// TestDropboxPutRechecksContextAfterSDKSuccess verifies post-upload cancellation remains observable.
func TestDropboxPutRechecksContextAfterSDKSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeDropbox{
		metaByPath: map[string]files.IsMetadata{
			"/pre": &files.FolderMetadata{Metadata: files.Metadata{PathLower: "/pre"}},
		},
		uploadHook: cancel,
	}
	d := &driver{client: client, prefix: "pre"}
	if err := d.PutContext(ctx, "file.txt", []byte("contents")); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext post-upload cancellation error = %v", err)
	}
}

// TestDropboxMakeDirAndMoveCreateParents verifies nested operations create missing destination folders.
func TestDropboxMakeDirAndMoveCreateParents(t *testing.T) {
	client := &fakeDropbox{metaByPath: make(map[string]files.IsMetadata)}
	d := &driver{client: client, prefix: "pre"}

	if err := d.MakeDirContext(context.Background(), "one/two"); err != nil {
		t.Fatalf("MakeDirContext: %v", err)
	}
	if got := createPaths(client.createArgs); strings.Join(got, ",") != "/pre,/pre/one,/pre/one/two" {
		t.Fatalf("MakeDirContext created = %v", got)
	}

	client.createArgs = nil
	client.metaByPath["/pre/source.txt"] = &files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/source.txt"}}
	if err := d.MoveContext(context.Background(), "source.txt", "three/four/destination.txt"); err != nil {
		t.Fatalf("MoveContext: %v", err)
	}
	if got := createPaths(client.createArgs); strings.Join(got, ",") != "/pre/three,/pre/three/four" {
		t.Fatalf("MoveContext created = %v", got)
	}
	if len(client.moveArgs) != 1 || client.moveArgs[0].FromPath != "/pre/source.txt" || client.moveArgs[0].ToPath != "/pre/three/four/destination.txt" {
		t.Fatalf("MoveV2 args = %+v", client.moveArgs)
	}
}

// TestDropboxDeleteRejectsRootAndDirectories verifies deletion is file-only and never targets the logical root.
func TestDropboxDeleteRejectsRootAndDirectories(t *testing.T) {
	for _, prefix := range []string{"", "pre"} {
		t.Run("root prefix "+prefix, func(t *testing.T) {
			client := &fakeDropbox{}
			d := &driver{client: client, prefix: prefix}
			if err := d.DeleteContext(context.Background(), ""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("DeleteContext root error = %v", err)
			}
			if len(client.deleteArgs) != 0 || len(client.metadataArgs) != 0 {
				t.Fatalf("Dropbox API called for root delete: metadata=%d delete=%d", len(client.metadataArgs), len(client.deleteArgs))
			}
		})
	}

	for _, name := range []string{"empty", "nonempty"} {
		t.Run(name+" directory", func(t *testing.T) {
			client := &fakeDropbox{metaByPath: map[string]files.IsMetadata{
				"/folder": &files.FolderMetadata{Metadata: files.Metadata{PathLower: "/folder"}},
			}}
			d := &driver{client: client}
			if err := d.DeleteContext(context.Background(), "folder"); !errors.Is(err, storagecore.ErrUnsupported) {
				t.Fatalf("DeleteContext directory error = %v", err)
			}
			if len(client.deleteArgs) != 0 || len(client.listArgs) != 0 {
				t.Fatalf("recursive delete path reached: list=%d delete=%d", len(client.listArgs), len(client.deleteArgs))
			}
		})
	}
}

// TestDropboxDeleteUsesRevisionGuard verifies a path replacement cannot turn file deletion into recursive folder deletion.
func TestDropboxDeleteUsesRevisionGuard(t *testing.T) {
	conflict := files.DeleteV2APIError{
		EndpointError: &files.DeleteError{
			Tagged:    dropbox.Tagged{Tag: files.DeleteErrorPathWrite},
			PathWrite: &files.WriteError{Tagged: dropbox.Tagged{Tag: files.WriteErrorConflict}},
		},
	}
	client := &fakeDropbox{
		metaOut: &files.FileMetadata{Metadata: files.Metadata{PathLower: "/file.txt"}, Rev: "rev-1"},
		deleteHook: func(arg *files.DeleteArg) error {
			if arg.ParentRev != "rev-1" {
				t.Fatalf("DeleteV2 ParentRev = %q", arg.ParentRev)
			}
			// This conflict models the file becoming a folder after metadata lookup.
			return conflict
		},
	}
	d := &driver{client: client}
	if err := d.DeleteContext(context.Background(), "file.txt"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("DeleteContext replacement conflict error = %v", err)
	}
	if len(client.metadataArgs) != 1 || len(client.deleteArgs) != 1 {
		t.Fatalf("delete calls: metadata=%d delete=%d", len(client.metadataArgs), len(client.deleteArgs))
	}
}

// TestDropboxMoveValidatesSourceBeforeCreatingParents verifies absent sources cause no destination mutations.
func TestDropboxMoveValidatesSourceBeforeCreatingParents(t *testing.T) {
	client := &fakeDropbox{metaByPath: make(map[string]files.IsMetadata)}
	d := &driver{client: client}
	if err := d.MoveContext(context.Background(), "missing.txt", "new/parent/file.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("MoveContext missing source error = %v", err)
	}
	if len(client.createArgs) != 0 || len(client.moveArgs) != 0 {
		t.Fatalf("MoveContext mutated destination for missing source: create=%d move=%d", len(client.createArgs), len(client.moveArgs))
	}

	client.metaByPath["/folder"] = &files.FolderMetadata{Metadata: files.Metadata{PathLower: "/folder"}}
	if err := d.MoveContext(context.Background(), "folder", "folder/child/destination"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("MoveContext nested destination error = %v", err)
	}
	if len(client.createArgs) != 0 || len(client.moveArgs) != 0 {
		t.Fatalf("MoveContext mutated nested destination: create=%d move=%d", len(client.createArgs), len(client.moveArgs))
	}
}

// TestDropboxMoveRejectsCaseInsensitiveDescendants verifies Dropbox's case-folded namespace cannot move into itself.
func TestDropboxMoveRejectsCaseInsensitiveDescendants(t *testing.T) {
	client := &fakeDropbox{metaByPath: map[string]files.IsMetadata{
		"/Foo": &files.FolderMetadata{Metadata: files.Metadata{PathDisplay: "/Foo"}},
	}}
	d := &driver{client: client}
	if err := d.MoveContext(context.Background(), "Foo", "foo/child"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("MoveContext folded descendant error = %v", err)
	}
	if len(client.createArgs) != 0 || len(client.moveArgs) != 0 {
		t.Fatal("MoveContext mutated Dropbox for a folded descendant")
	}

	client.metaByPath["/foo"] = client.metaByPath["/Foo"]
	if err := d.MoveContext(context.Background(), "Foo", "foo"); err != nil {
		t.Fatalf("MoveContext case-only rename: %v", err)
	}
	if len(client.moveArgs) != 1 || client.moveArgs[0].FromPath != "/Foo" || client.moveArgs[0].ToPath != "/foo" {
		t.Fatalf("case-only MoveV2 args = %+v", client.moveArgs)
	}
}

// TestDropboxUsesEmptyAPIPathForRoot verifies Dropbox API calls encode the backend root as an empty path.
func TestDropboxUsesEmptyAPIPathForRoot(t *testing.T) {
	client := &fakeDropbox{}
	d := &driver{client: client}
	if _, err := d.ListContext(context.Background(), ""); err != nil {
		t.Fatalf("ListContext root: %v", err)
	}
	if len(client.listArgs) != 1 || client.listArgs[0].Path != "" {
		t.Fatalf("ListFolder root args = %+v", client.listArgs)
	}
}

// TestDropboxMissingPrefixedRootIsEmpty verifies an absent configured prefix behaves as an empty namespace.
func TestDropboxMissingPrefixedRootIsEmpty(t *testing.T) {
	client := &fakeDropbox{listErr: errNotFound{}}
	d := &driver{client: client, prefix: "pre"}
	entries, err := d.ListContext(context.Background(), "")
	if err != nil || len(entries) != 0 {
		t.Fatalf("ListContext missing root = %+v, %v", entries, err)
	}
	called := false
	if err := d.WalkContext(context.Background(), "", func(storagecore.Entry) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("WalkContext missing root: %v", err)
	}
	if called {
		t.Fatal("WalkContext called callback for missing logical root")
	}
}

// TestDropboxLogicalRootGuards verifies synthetic-root reads and mutations cannot alias a backend object.
func TestDropboxLogicalRootGuards(t *testing.T) {
	for _, prefix := range []string{"", "pre"} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			client := &fakeDropbox{metaByPath: make(map[string]files.IsMetadata)}
			d := &driver{client: client, prefix: prefix}
			if err := d.PutContext(context.Background(), "", []byte("x")); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("PutContext root error = %v", err)
			}
			if err := d.MakeDirContext(context.Background(), ""); err != nil {
				t.Fatalf("MakeDirContext root: %v", err)
			}
			if err := d.CopyContext(context.Background(), "", "copy.txt"); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("CopyContext source root error = %v", err)
			}
			if err := d.CopyContext(context.Background(), "source.txt", ""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("CopyContext destination root error = %v", err)
			}
			if err := d.MoveContext(context.Background(), "source.txt", ""); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("MoveContext destination root error = %v", err)
			}
			if err := d.MoveContext(context.Background(), "", "destination.txt"); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("MoveContext source root error = %v", err)
			}
			entry, err := d.StatContext(context.Background(), "")
			if err != nil || !entry.IsDir || entry.Path != "" {
				t.Fatalf("StatContext root = %+v, %v", entry, err)
			}
			if len(client.uploadArgs) != 0 || len(client.moveArgs) != 0 || len(client.createArgs) != 0 {
				t.Fatalf("mutating API called for logical root: upload=%d move=%d create=%d", len(client.uploadArgs), len(client.moveArgs), len(client.createArgs))
			}
		})
	}
}

// TestDropboxCopyAndMoveSamePathAreNoOps verifies normalized no-ops validate sources without mutation.
func TestDropboxCopyAndMoveSamePathAreNoOps(t *testing.T) {
	for _, prefix := range []string{"", "pre"} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			full := "/File.txt"
			if prefix != "" {
				full = "/" + prefix + "/File.txt"
			}
			client := &fakeDropbox{
				getData: "contents",
				metaByPath: map[string]files.IsMetadata{
					full: &files.FileMetadata{Metadata: files.Metadata{PathDisplay: full}},
				},
			}
			d := &driver{client: client, prefix: prefix}
			if err := d.CopyContext(context.Background(), "folder/../File.txt", "File.txt"); err != nil {
				t.Fatalf("CopyContext same path: %v", err)
			}
			if err := d.MoveContext(context.Background(), "File.txt", "File.txt"); err != nil {
				t.Fatalf("MoveContext same path: %v", err)
			}
			if len(client.downloadArgs) != 1 || len(client.metadataArgs) != 1 {
				t.Fatalf("source validation calls: download=%d metadata=%d", len(client.downloadArgs), len(client.metadataArgs))
			}
			if len(client.uploadArgs)+len(client.moveArgs)+len(client.createArgs) != 0 {
				t.Fatal("Dropbox mutation API called for same-path operation")
			}
		})
	}

	missing := &driver{client: &fakeDropbox{getErr: errNotFound{}}}
	if err := missing.CopyContext(context.Background(), "missing.txt", "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("CopyContext missing same path error = %v", err)
	}
	missing.client = &fakeDropbox{metaErr: errNotFound{}}
	if err := missing.MoveContext(context.Background(), "missing.txt", "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("MoveContext missing same path error = %v", err)
	}
}

// TestDropboxPreservesDisplayPathCasing verifies logical results retain Dropbox display casing.
func TestDropboxPreservesDisplayPathCasing(t *testing.T) {
	metadata := files.Metadata{PathLower: "/root/mixed.txt", PathDisplay: "/RoOt/MiXeD.txt"}
	client := &fakeDropbox{
		metaByPath: map[string]files.IsMetadata{
			"/root/mixed.txt": &files.FileMetadata{Metadata: metadata, Size: 7},
		},
		listOut: &files.ListFolderResult{Entries: []files.IsMetadata{
			&files.FileMetadata{Metadata: metadata, Size: 7},
		}},
	}
	d := &driver{client: client, prefix: "root"}
	entry, err := d.StatContext(context.Background(), "mixed.txt")
	if err != nil {
		t.Fatalf("StatContext: %v", err)
	}
	if entry.Path != "MiXeD.txt" {
		t.Fatalf("StatContext path = %q", entry.Path)
	}
	entries, err := d.ListContext(context.Background(), "")
	if err != nil {
		t.Fatalf("ListContext: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "MiXeD.txt" {
		t.Fatalf("ListContext entries = %+v", entries)
	}
}

// TestDropboxTypedErrors verifies structured lookup and authorization failures map portably.
func TestDropboxTypedErrors(t *testing.T) {
	notFound := files.GetMetadataAPIError{
		APIError: dropbox.APIError{ErrorSummary: "path/not_found"},
		EndpointError: &files.GetMetadataError{
			Tagged: dropbox.Tagged{Tag: files.GetMetadataErrorPath},
			Path:   &files.LookupError{Tagged: dropbox.Tagged{Tag: files.LookupErrorNotFound}},
		},
	}
	if err := wrapError(notFound); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("typed not-found error = %v", err)
	}

	forbidden := files.UploadAPIError{
		APIError: dropbox.APIError{ErrorSummary: "path/no_write_permission"},
		EndpointError: &files.UploadError{
			Tagged: dropbox.Tagged{Tag: files.UploadErrorPath},
			Path: &files.UploadWriteFailed{
				Reason: &files.WriteError{Tagged: dropbox.Tagged{Tag: files.WriteErrorNoWritePermission}},
			},
		},
	}
	if err := wrapError(forbidden); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("typed forbidden error = %v", err)
	}
	if err := wrapError(dropbox.SDKInternalError{StatusCode: 403}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("HTTP forbidden error = %v", err)
	}
}

// TestDropboxListContinuationIsIterative verifies long pagination chains do not consume call stack.
func TestDropboxListContinuationIsIterative(t *testing.T) {
	const pages = 10000
	sequence := make([]*files.ListFolderResult, pages)
	for i := range sequence {
		sequence[i] = &files.ListFolderResult{HasMore: i < pages-1, Cursor: "next"}
	}
	client := &fakeDropbox{continueSeq: sequence}
	d := &driver{client: client}
	var entries []storagecore.Entry
	if err := d.listContinue(context.Background(), files.NewListFolderContinueArg("first"), &entries); err != nil {
		t.Fatalf("listContinue: %v", err)
	}
	if len(client.continueArgs) != pages {
		t.Fatalf("continuation calls = %d, want %d", len(client.continueArgs), pages)
	}
}

// createPaths projects recorded folder arguments into their API paths for assertions.
func createPaths(args []*files.CreateFolderArg) []string {
	paths := make([]string, len(args))
	for i, arg := range args {
		paths[i] = arg.Path
	}
	return paths
}
