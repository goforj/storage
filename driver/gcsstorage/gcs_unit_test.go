package gcsstorage

import (
	"context"
	"errors"
	"testing"

	gcsapi "cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"github.com/goforj/storage"
)

func TestGCSConstructors(t *testing.T) {
	t.Run("new missing bucket", func(t *testing.T) {
		_, err := New(Config{})
		if err == nil {
			t.Fatal("New returned nil error")
		}
	})

	t.Run("url emulator unsupported", func(t *testing.T) {
		d := &driver{emulator: true}
		_, err := d.URLContext(context.Background(), "file.txt")
		if !errors.Is(err, storage.ErrUnsupported) {
			t.Fatalf("URLContext error = %v", err)
		}
	})
}

func TestGCSKeyAndPrefixHelpers(t *testing.T) {
	d := &driver{prefix: "pre"}
	k, err := d.key("file.txt")
	if err != nil {
		t.Fatalf("key err: %v", err)
	}
	if k != "pre/file.txt" {
		t.Fatalf("key got %q", k)
	}
	if got := d.stripPrefix("pre/dir/file"); got != "dir/file" {
		t.Fatalf("stripPrefix got %q", got)
	}
}

func TestGCSWrapError(t *testing.T) {
	if err := wrapError(gcsapi.ErrObjectNotExist); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if !isNotFound(&googleapi.Error{Code: 404}) {
		t.Fatal("isNotFound should detect googleapi 404")
	}
}

func TestGCSContextCancellation(t *testing.T) {
	d := &driver{prefix: "pre", emulator: false}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := d.GetContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetContext error = %v", err)
	}
	if err := d.PutContext(ctx, "file.txt", []byte("hello")); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext error = %v", err)
	}
	if err := d.DeleteContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext error = %v", err)
	}
	if _, err := d.ExistsContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExistsContext error = %v", err)
	}
	if _, err := d.ListContext(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext error = %v", err)
	}
	if err := d.WalkContext(ctx, "", func(storage.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext error = %v", err)
	}
	if _, err := d.URLContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
}
