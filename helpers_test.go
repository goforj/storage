package filesystem

import (
	"context"
	"testing"
)

type fakeFS struct {
	lastCtx      context.Context
	lastPath     string
	lastContents []byte

	getData []byte
	exists  bool
	list    []Entry
	url     string
	err     error
}

func (f *fakeFS) record(ctx context.Context, p string, contents []byte) {
	f.lastCtx = ctx
	f.lastPath = p
	f.lastContents = contents
}

func (f *fakeFS) Get(ctx context.Context, p string) ([]byte, error) {
	f.record(ctx, p, nil)
	return f.getData, f.err
}

func (f *fakeFS) Put(ctx context.Context, p string, contents []byte) error {
	f.record(ctx, p, contents)
	return f.err
}

func (f *fakeFS) Delete(ctx context.Context, p string) error {
	f.record(ctx, p, nil)
	return f.err
}

func (f *fakeFS) Exists(ctx context.Context, p string) (bool, error) {
	f.record(ctx, p, nil)
	return f.exists, f.err
}

func (f *fakeFS) List(ctx context.Context, p string) ([]Entry, error) {
	f.record(ctx, p, nil)
	return f.list, f.err
}

func (f *fakeFS) URL(ctx context.Context, p string) (string, error) {
	f.record(ctx, p, nil)
	return f.url, f.err
}

func TestFSWithHelpersUsesBackgroundContext(t *testing.T) {
	fs := &fakeFS{
		getData: []byte("data"),
		exists:  true,
		list:    []Entry{{Path: "a"}},
		url:     "u://p",
	}

	w := WithHelpers(fs)

	// Get
	data, err := w.Get("g")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if string(data) != "data" {
		t.Fatalf("Get data = %s", data)
	}
	if fs.lastCtx != context.Background() || fs.lastPath != "g" {
		t.Fatalf("Get did not use background ctx or path; ctx=%v path=%s", fs.lastCtx, fs.lastPath)
	}

	// Put
	if err := w.Put("p", []byte("body")); err != nil {
		t.Fatalf("Put error: %v", err)
	}
	if fs.lastCtx != context.Background() || fs.lastPath != "p" {
		t.Fatalf("Put did not use background ctx or path; ctx=%v path=%s", fs.lastCtx, fs.lastPath)
	}
	if string(fs.lastContents) != "body" {
		t.Fatalf("Put contents = %s", fs.lastContents)
	}

	// Delete
	if err := w.Delete("d"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if fs.lastCtx != context.Background() || fs.lastPath != "d" {
		t.Fatalf("Delete did not use background ctx or path; ctx=%v path=%s", fs.lastCtx, fs.lastPath)
	}

	// Exists
	exists, err := w.Exists("e")
	if err != nil {
		t.Fatalf("Exists error: %v", err)
	}
	if !exists {
		t.Fatalf("Exists = false, want true")
	}
	if fs.lastCtx != context.Background() || fs.lastPath != "e" {
		t.Fatalf("Exists did not use background ctx or path; ctx=%v path=%s", fs.lastCtx, fs.lastPath)
	}

	// List
	list, err := w.List("l")
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(list) != 1 || list[0].Path != "a" {
		t.Fatalf("List = %+v", list)
	}
	if fs.lastCtx != context.Background() || fs.lastPath != "l" {
		t.Fatalf("List did not use background ctx or path; ctx=%v path=%s", fs.lastCtx, fs.lastPath)
	}

	// URL
	u, err := w.URL("u")
	if err != nil {
		t.Fatalf("URL error: %v", err)
	}
	if u != "u://p" {
		t.Fatalf("URL = %s", u)
	}
	if fs.lastCtx != context.Background() || fs.lastPath != "u" {
		t.Fatalf("URL did not use background ctx or path; ctx=%v path=%s", fs.lastCtx, fs.lastPath)
	}
}
