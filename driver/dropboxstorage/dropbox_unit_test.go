package dropboxstorage

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/goforj/storage"
)

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
	getErr      error
	putErr      error
	delErr      error
	metaErr     error
	listErr     error
	listOut     *files.ListFolderResult
	linkErr     error
	linkURL     string
	continueOut *files.ListFolderResult
}

func (f *fakeDropbox) Download(arg *files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, nil, f.getErr
	}
	return &files.FileMetadata{}, io.NopCloser(strings.NewReader(f.getData)), nil
}
func (f *fakeDropbox) Upload(arg *files.UploadArg, content io.Reader) (*files.FileMetadata, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &files.FileMetadata{}, nil
}
func (f *fakeDropbox) DeleteV2(arg *files.DeleteArg) (*files.DeleteResult, error) {
	return nil, f.delErr
}
func (f *fakeDropbox) GetMetadata(arg *files.GetMetadataArg) (files.IsMetadata, error) {
	return nil, f.metaErr
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
	exists, err := d.Exists("file.txt")
	if err != nil || !exists {
		t.Fatalf("Exists err %v exists %v", err, exists)
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
