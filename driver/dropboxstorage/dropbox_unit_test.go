package dropboxstorage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/goforj/storage"
)

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
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not_found/.." }

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
}

func TestDropboxWrapError(t *testing.T) {
	if err := wrapError(errNotFound{}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound")
	}
	if err := wrapError(errors.New("other")); errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unexpected ErrNotFound")
	}
}

type fakeDropbox struct {
	getData     string
	getReader   io.ReadCloser
	getErr      error
	putErr      error
	delErr      error
	metaErr     error
	metaOut     files.IsMetadata
	listErr     error
	listOut     *files.ListFolderResult
	linkErr     error
	linkURL     string
	continueOut *files.ListFolderResult
	continueSeq []*files.ListFolderResult
	uploaded    []byte
}

func (f *fakeDropbox) Download(arg *files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, nil, f.getErr
	}
	if f.getReader != nil {
		return &files.FileMetadata{}, f.getReader, nil
	}
	return &files.FileMetadata{}, io.NopCloser(strings.NewReader(f.getData)), nil
}
func (f *fakeDropbox) Upload(arg *files.UploadArg, content io.Reader) (*files.FileMetadata, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	f.uploaded = data
	return &files.FileMetadata{}, nil
}
func (f *fakeDropbox) DeleteV2(arg *files.DeleteArg) (*files.DeleteResult, error) {
	return nil, f.delErr
}
func (f *fakeDropbox) GetMetadata(arg *files.GetMetadataArg) (files.IsMetadata, error) {
	if f.metaErr != nil {
		return nil, f.metaErr
	}
	return f.metaOut, nil
}
func (f *fakeDropbox) ListFolder(arg *files.ListFolderArg) (*files.ListFolderResult, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.listOut != nil {
		return f.listOut, nil
	}
	return &files.ListFolderResult{}, nil
}
func (f *fakeDropbox) ListFolderContinue(arg *files.ListFolderContinueArg) (*files.ListFolderResult, error) {
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
func (f *fakeDropbox) GetTemporaryLink(arg *files.GetTemporaryLinkArg) (*files.GetTemporaryLinkResult, error) {
	if f.linkErr != nil {
		return nil, f.linkErr
	}
	return &files.GetTemporaryLinkResult{Link: f.linkURL}, nil
}

func TestDropboxStorageOperations(t *testing.T) {
	client := &fakeDropbox{
		getData: "hello",
		metaOut: &files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/file.txt"}, Size: 5},
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
	if _, err := d.URL("file.txt"); err != nil {
		t.Fatalf("URL err: %v", err)
	}

	// deletion and not found
	client.delErr = errNotFound{}
	if err := d.Delete("missing.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected wrapped not found, got %v", err)
	}
}

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

	var got []storage.Entry
	if err := d.WalkContext(context.Background(), "", func(entry storage.Entry) error {
		got = append(got, entry)
		return nil
	}); err != nil {
		t.Fatalf("WalkContext: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("WalkContext entries = %v", got)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.emitWalkEntries(canceled, client.listOut.Entries, func(storage.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("emitWalkEntries canceled error = %v", err)
	}

	want := errors.New("stop")
	if err := d.emitWalkEntries(context.Background(), client.listOut.Entries, func(storage.Entry) error { return want }); !errors.Is(err, want) {
		t.Fatalf("emitWalkEntries callback error = %v", err)
	}
}

func TestDropboxListContinue(t *testing.T) {
	client := &fakeDropbox{
		continueOut: &files.ListFolderResult{
			Entries: []files.IsMetadata{
				&files.FileMetadata{Metadata: files.Metadata{PathLower: "/pre/file2.txt"}, Size: 4},
			},
		},
	}
	d := &driver{client: client, prefix: "pre"}

	var entries []storage.Entry
	if err := d.listContinue(context.Background(), files.NewListFolderContinueArg("cursor"), &entries); err != nil {
		t.Fatalf("listContinue: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "file2.txt" {
		t.Fatalf("listContinue entries = %+v", entries)
	}
}

func TestDropboxWrappersAndErrors(t *testing.T) {
	client := &fakeDropbox{
		getErr:  errors.New("boom"),
		putErr:  errors.New("boom"),
		delErr:  errors.New("boom"),
		metaErr: errors.New("boom"),
		linkErr: errors.New("boom"),
	}
	d := &driver{client: client, prefix: "pre"}

	if err := d.Walk("", func(storage.Entry) error { return nil }); err != nil {
		t.Fatalf("Walk wrapper: %v", err)
	}

	if _, err := d.GetContext(context.Background(), "../bad"); !errors.Is(err, storage.ErrForbidden) {
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
	if _, err := d.ExistsContext(context.Background(), "../bad"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("ExistsContext invalid path error = %v", err)
	}
	if _, err := d.ListContext(context.Background(), "../bad"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("ListContext invalid path error = %v", err)
	}
	if err := d.WalkContext(context.Background(), "../bad", func(storage.Entry) error { return nil }); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("WalkContext invalid path error = %v", err)
	}
}

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
	})

	t.Run("unsupported metadata", func(t *testing.T) {
		d := &driver{
			client: &fakeDropbox{
				metaOut: &files.DeletedMetadata{Metadata: files.Metadata{PathLower: "/pre/deleted"}},
			},
			prefix: "pre",
		}
		if _, err := d.StatContext(context.Background(), "deleted"); !errors.Is(err, storage.ErrUnsupported) {
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

func TestDropboxCopyAndMove(t *testing.T) {
	client := &fakeDropbox{
		getData: "payload",
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

	client.delErr = errors.New("delete boom")
	if err := d.MoveContext(context.Background(), "src.txt", "broken.txt"); err == nil {
		t.Fatal("MoveContext returned nil error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.CopyContext(ctx, "src.txt", "dst.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext canceled error = %v", err)
	}
}

func TestDropboxListAndWalkErrors(t *testing.T) {
	client := &fakeDropbox{listErr: errors.New("boom")}
	d := &driver{client: client, prefix: "pre"}
	if _, err := d.ListContext(context.Background(), "folder"); err == nil {
		t.Fatal("ListContext returned nil error")
	}
	if err := d.walkPage(context.Background(), files.NewListFolderArg("/pre"), func(storage.Entry) error { return nil }); err == nil {
		t.Fatal("walkPage returned nil error")
	}

	client = &fakeDropbox{continueOut: nil, listOut: &files.ListFolderResult{HasMore: true, Cursor: "cursor"}}
	d = &driver{client: client, prefix: "pre"}
	if err := d.WalkContext(context.Background(), "", func(storage.Entry) error { return nil }); err != nil {
		t.Fatalf("WalkContext empty continue: %v", err)
	}

	client = &fakeDropbox{}
	d = &driver{client: client, prefix: "pre"}
	var entries []storage.Entry
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

func TestDropboxGetContextReadFailure(t *testing.T) {
	client := &fakeDropbox{
		getReader: io.NopCloser(errReader{}),
	}
	d := &driver{client: client, prefix: "pre"}
	if _, err := d.GetContext(context.Background(), "file.txt"); err == nil {
		t.Fatal("GetContext returned nil error")
	}
}

func TestDropboxExistsFalseOnNotFound(t *testing.T) {
	d := &driver{client: &fakeDropbox{metaErr: errNotFound{}}, prefix: "pre"}
	ok, err := d.Exists("missing.txt")
	if err != nil || ok {
		t.Fatalf("Exists missing = %v err=%v", ok, err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }
