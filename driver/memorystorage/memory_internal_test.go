package memorystorage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// TestMemoryRemainingBranches exercises validation, pagination, directory, and
// helper branches that are deliberately outside the shared storage contract.
func TestMemoryRemainingBranches(t *testing.T) {
	d := &driver{prefix: "tenant", dirs: make(map[string]struct{}), objects: make(map[string]object)}

	if _, err := newFromDiskConfig(nil, storagecore.ResolvedConfig{Prefix: "../bad"}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("newFromDiskConfig invalid prefix error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newFromDiskConfig(ctx, storagecore.ResolvedConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("newFromDiskConfig canceled error = %v", err)
	}

	for name, call := range map[string]func() error{
		"get":              func() error { _, err := d.GetContext(nil, "../bad"); return err },
		"make directory":   func() error { return d.MakeDirContext(nil, "../bad") },
		"delete":           func() error { return d.DeleteContext(nil, "../bad") },
		"stat":             func() error { _, err := d.StatContext(nil, "../bad"); return err },
		"exists":           func() error { _, err := d.ExistsContext(nil, "../bad"); return err },
		"list":             func() error { _, err := d.ListContext(nil, "../bad"); return err },
		"list page":        func() error { _, err := d.ListPageContext(nil, "../bad", 0, 1); return err },
		"walk":             func() error { return d.WalkContext(nil, "../bad", func(storagecore.Entry) error { return nil }) },
		"copy source":      func() error { return d.CopyContext(nil, "../bad", "dst") },
		"copy destination": func() error { return d.CopyContext(nil, "src", "../bad") },
		"move source":      func() error { return d.MoveContext(nil, "../bad", "dst") },
		"move destination": func() error { return d.MoveContext(nil, "src", "../bad") },
		"mod time":         func() error { _, err := d.ModTime(nil, "../bad"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := d.WalkContext(nil, "", nil); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("WalkContext nil callback error = %v", err)
	}
	if err := d.MakeDir("nested/empty"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if err := d.Delete("nested"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Delete non-empty directory error = %v", err)
	}
	if err := d.Delete("nested/empty"); err != nil {
		t.Fatalf("Delete empty directory: %v", err)
	}
	if err := d.Put("file", []byte("x")); err != nil {
		t.Fatalf("Put file: %v", err)
	}
	if page, err := d.ListPage("", -1, 0); err != nil || page.Offset != 0 || page.Limit != 100 {
		t.Fatalf("ListPage defaults = %+v, %v", page, err)
	}
	if page, err := d.ListPage("", 99, 1); err != nil || len(page.Entries) != 0 {
		t.Fatalf("ListPage bounded offset = %+v, %v", page, err)
	}
	if _, err := d.ListPage("file", 0, 1); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("ListPage object error = %v", err)
	}
	if err := d.Walk("file", func(entry storagecore.Entry) error {
		if entry.Path != "file" {
			t.Fatalf("Walk file entry = %+v", entry)
		}
		return nil
	}); err != nil {
		t.Fatalf("Walk file: %v", err)
	}

	dirs := recursiveParentDirs("a/b/c")
	if !reflect.DeepEqual(dirs, []string{"a", "a/b"}) {
		t.Fatalf("recursiveParentDirs = %v", dirs)
	}
	if recursiveParentDirs("") != nil || recursiveParentDirs("one") != nil {
		t.Fatal("recursiveParentDirs should omit paths without parents")
	}
	if d.stripPrefix("tenant") != "" || d.stripPrefix("tenant/file") != "file" {
		t.Fatalf("stripPrefix returned unexpected values")
	}
}

// TestPutContextRechecksCancellationAfterLockWait proves cancellation wins
// when it arrives after validation but before a contended mutation can run.
func TestPutContextRechecksCancellationAfterLockWait(t *testing.T) {
	d := &driver{
		dirs:    make(map[string]struct{}),
		objects: make(map[string]object),
	}
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedContext{Context: base, checked: make(chan struct{})}

	d.mu.Lock()
	result := make(chan error, 1)
	go func() {
		result <- d.PutContext(ctx, "blocked.txt", []byte("payload"))
	}()
	<-ctx.checked
	cancel()
	d.mu.Unlock()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext error = %v", err)
	}
	d.mu.RLock()
	_, exists := d.objects["blocked.txt"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("PutContext mutated storage after cancellation")
	}
}

// TestSamePathCopyPreservesObject verifies a validated no-op leaves private metadata unchanged.
func TestSamePathCopyPreservesObject(t *testing.T) {
	d := &driver{
		dirs:    make(map[string]struct{}),
		objects: make(map[string]object),
	}
	if err := d.Put("file.txt", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	before := d.objects["file.txt"].modTime
	if err := d.Copy("file.txt", "file.txt"); err != nil {
		t.Fatalf("Copy same path: %v", err)
	}
	after := d.objects["file.txt"].modTime
	if !after.Equal(before) {
		t.Fatalf("same-path Copy changed modtime from %v to %v", before, after)
	}
	if err := d.Copy("missing.txt", "missing.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Copy missing same path error = %v", err)
	}
}

// TestMemoryCollisionAndMissingBranches verifies each path-shape conflict and
// absence distinction exposed by the in-memory implementation.
func TestMemoryCollisionAndMissingBranches(t *testing.T) {
	d := &driver{dirs: make(map[string]struct{}), objects: make(map[string]object)}
	if err := d.MakeDir(""); err != nil {
		t.Fatalf("MakeDir root: %v", err)
	}
	if err := d.Put("parent", []byte("file")); err != nil {
		t.Fatalf("Put parent: %v", err)
	}
	if err := d.Put("parent/child", nil); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Put beneath object error = %v", err)
	}
	if err := d.MakeDir("parent/child"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("MakeDir beneath object error = %v", err)
	}
	if err := d.MakeDir("directory"); err != nil {
		t.Fatalf("MakeDir directory: %v", err)
	}
	if err := d.Put("directory", nil); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Put over directory error = %v", err)
	}
	if err := d.Put("directory/child", nil); err != nil {
		t.Fatalf("Put child: %v", err)
	}
	if err := d.Put("directory", nil); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Put over implied children error = %v", err)
	}
	if _, err := d.Get("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Get missing error = %v", err)
	}
	if _, err := d.Stat("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Stat missing error = %v", err)
	}
	if _, err := d.List("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("List missing error = %v", err)
	}
	if _, err := d.ListPage("missing", 0, 1); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("ListPage missing error = %v", err)
	}
	if err := d.Copy("missing", "copy"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Copy missing error = %v", err)
	}
	if err := d.Copy("parent", "directory"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Copy onto directory error = %v", err)
	}
	if err := d.Move("parent", "directory"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Move onto directory error = %v", err)
	}
	if _, err := d.ModTime(context.Background(), "missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("ModTime missing error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.ModTime(canceled, "parent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ModTime canceled error = %v", err)
	}
	if _, err := d.URL("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("URL missing error = %v", err)
	}
	if _, err := d.URL("parent"); !errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("URL existing error = %v", err)
	}

	d.objects["implicit/file"] = object{data: []byte("x")}
	entry, err := d.Stat("implicit")
	if err != nil || !entry.IsDir {
		t.Fatalf("Stat implicit directory = %+v, %v", entry, err)
	}
	if !d.hasChildrenLocked("implicit") {
		t.Fatal("hasChildrenLocked did not find object descendant")
	}
	d.dirs["self"] = struct{}{}
	if entries := d.listEntriesLocked("self"); len(entries) != 0 {
		t.Fatalf("listEntriesLocked self = %+v", entries)
	}

	walkCtx, walkCancel := context.WithCancel(context.Background())
	seen := 0
	err = d.WalkContext(walkCtx, "", func(storagecore.Entry) error {
		seen++
		walkCancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || seen != 1 {
		t.Fatalf("WalkContext cancellation = %v after %d entries", err, seen)
	}
}

// TestMemoryPostLockCancellation verifies every locked operation rechecks the
// caller context after waiting for concurrent storage work.
func TestMemoryPostLockCancellation(t *testing.T) {
	tests := []struct {
		name string
		call func(*driver, context.Context) error
	}{
		{name: "get", call: func(d *driver, ctx context.Context) error { _, err := d.GetContext(ctx, "file"); return err }},
		{name: "put", call: func(d *driver, ctx context.Context) error { return d.PutContext(ctx, "file", nil) }},
		{name: "mkdir", call: func(d *driver, ctx context.Context) error { return d.MakeDirContext(ctx, "dir") }},
		{name: "delete", call: func(d *driver, ctx context.Context) error { return d.DeleteContext(ctx, "file") }},
		{name: "stat", call: func(d *driver, ctx context.Context) error { _, err := d.StatContext(ctx, "file"); return err }},
		{name: "exists", call: func(d *driver, ctx context.Context) error { _, err := d.ExistsContext(ctx, "file"); return err }},
		{name: "list", call: func(d *driver, ctx context.Context) error { _, err := d.ListContext(ctx, ""); return err }},
		{name: "list page", call: func(d *driver, ctx context.Context) error { _, err := d.ListPageContext(ctx, "", 0, 1); return err }},
		{name: "walk", call: func(d *driver, ctx context.Context) error {
			return d.WalkContext(ctx, "", func(storagecore.Entry) error { return nil })
		}},
		{name: "copy", call: func(d *driver, ctx context.Context) error { return d.CopyContext(ctx, "file", "copy") }},
		{name: "mod time", call: func(d *driver, ctx context.Context) error { _, err := d.ModTime(ctx, "file"); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := &driver{dirs: make(map[string]struct{}), objects: map[string]object{"file": {}}}
			base, cancel := context.WithCancel(context.Background())
			ctx := &observedContext{Context: base, checked: make(chan struct{})}
			d.mu.Lock()
			result := make(chan error, 1)
			go func() { result <- test.call(d, ctx) }()
			<-ctx.checked
			cancel()
			d.mu.Unlock()
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type observedContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

// Err reports the wrapped context state and exposes when the operation has
// completed its initial cancellation check.
func (c *observedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}
