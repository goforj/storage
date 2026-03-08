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

	t.Run("new invalid prefix", func(t *testing.T) {
		_, err := New(Config{Bucket: "bucket", Prefix: "../bad"})
		if !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("New invalid prefix error = %v", err)
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
	if err := wrapError(errors.New("other")); errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("wrapError should preserve non-not-found errors")
	}
	if isNotFound(errors.New("other")) {
		t.Fatal("isNotFound should ignore other errors")
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
	if err := d.CopyContext(ctx, "file.txt", "copy.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext error = %v", err)
	}
	if err := d.MoveContext(ctx, "file.txt", "moved.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext error = %v", err)
	}
	if _, err := d.URLContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
}

func TestGCSHelpers(t *testing.T) {
	cfg := Config{Bucket: "bucket", CredentialsJSON: "{}", Endpoint: "http://127.0.0.1:4443", Prefix: "pre"}
	resolved := cfg.ResolvedConfig()
	if cfg.DriverName() != "gcs" || resolved.GCSBucket != "bucket" || resolved.Prefix != "pre" || resolved.GCSEndpoint == "" {
		t.Fatalf("ResolvedConfig = %+v", resolved)
	}

	d := &driver{}
	if got := d.stripPrefix("plain/path"); got != "plain/path" {
		t.Fatalf("stripPrefix without prefix = %q", got)
	}
	if _, err := d.key("../bad"); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("key invalid error = %v", err)
	}
	if got := recursiveParentDirs("a/b/c/file.txt"); len(got) != 3 || got[0] != "a" || got[2] != "a/b/c" {
		t.Fatalf("recursiveParentDirs = %v", got)
	}
	if dirs := recursiveParentDirs("file.txt"); dirs != nil {
		t.Fatalf("recursiveParentDirs file = %v", dirs)
	}
}
