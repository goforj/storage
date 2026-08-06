package redisstorage

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/goforj/storage/storagecore"
)

// TestConfigResolvedConfig verifies every Redis config field survives shared resolution.
func TestConfigResolvedConfig(t *testing.T) {
	cfg := Config{
		Addr:     "127.0.0.1:6379",
		Username: "user",
		Password: "pass",
		DB:       2,
		Prefix:   "sandbox",
	}
	resolved := cfg.ResolvedConfig()
	if resolved.Driver != "redis" {
		t.Fatalf("Driver = %q", resolved.Driver)
	}
	if resolved.RedisAddr != "127.0.0.1:6379" {
		t.Fatalf("RedisAddr = %q", resolved.RedisAddr)
	}
	if resolved.RedisUsername != "user" {
		t.Fatalf("RedisUsername = %q", resolved.RedisUsername)
	}
	if resolved.RedisPassword != "pass" {
		t.Fatalf("RedisPassword = %q", resolved.RedisPassword)
	}
	if resolved.RedisDB != 2 {
		t.Fatalf("RedisDB = %d", resolved.RedisDB)
	}
	if resolved.Prefix != "sandbox" {
		t.Fatalf("Prefix = %q", resolved.Prefix)
	}
}

// TestNewRequiresAddr rejects configuration before attempting an unusable client connection.
func TestNewRequiresAddr(t *testing.T) {
	_, err := New(Config{})
	if err == nil || err.Error() != "storage: redis storage requires RedisAddr" {
		t.Fatalf("New error = %v", err)
	}
}

// TestContextCancellation verifies every context-aware operation short-circuits before client access.
func TestContextCancellation(t *testing.T) {
	store := &driver{prefix: "itest"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.GetContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetContext error = %v", err)
	}
	if err := store.PutContext(ctx, "file.txt", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutContext error = %v", err)
	}
	if err := store.DeleteContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext error = %v", err)
	}
	if _, err := store.StatContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("StatContext error = %v", err)
	}
	if _, err := store.ExistsContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExistsContext error = %v", err)
	}
	if _, err := store.ListContext(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext error = %v", err)
	}
	if err := store.WalkContext(ctx, "", func(storagecore.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkContext error = %v", err)
	}
	if err := store.CopyContext(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyContext error = %v", err)
	}
	if err := store.MoveContext(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveContext error = %v", err)
	}
	if _, err := store.URLContext(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
	if _, err := store.ModTime(ctx, "file.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ModTime error = %v", err)
	}
}

// TestKeyHelpers verifies prefix insertion and removal preserve caller-visible paths.
func TestKeyHelpers(t *testing.T) {
	store := &driver{prefix: "sandbox"}

	key, err := store.key("dir/file.txt")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if key != "sandbox/dir/file.txt" {
		t.Fatalf("key = %q", key)
	}
	if got := store.stripPrefix("sandbox/dir/file.txt"); got != "dir/file.txt" {
		t.Fatalf("stripPrefix = %q", got)
	}
}

// TestRecursiveParentDirs verifies traversal synthesizes ancestors from root to direct parent.
func TestRecursiveParentDirs(t *testing.T) {
	got := recursiveParentDirs("one/two/file.txt")
	want := []string{"one", "one/two"}
	if len(got) != len(want) {
		t.Fatalf("recursiveParentDirs len = %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recursiveParentDirs[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

// TestRedisNamespace preserves the historical DB-zero keyspace while isolating nonzero databases.
func TestRedisNamespace(t *testing.T) {
	if got := redisNamespace(storagecore.ResolvedConfig{}); got != "goforj:storage:redis" {
		t.Fatalf("redisNamespace default = %q", got)
	}
	if got := redisNamespace(storagecore.ResolvedConfig{RedisDB: 3}); got != "goforj:storage:redis:db:3" {
		t.Fatalf("redisNamespace db = %q", got)
	}
}

// TestRedisInvalidPathsAndThinWrappers covers validation before any Redis
// request and the background-context adapters omitted by the shared contract.
func TestRedisInvalidPathsAndThinWrappers(t *testing.T) {
	d := &driver{}
	calls := []func() error{
		func() error { _, err := d.GetContext(nil, "../bad"); return err },
		func() error { return d.PutContext(nil, "../bad", nil) },
		func() error { return d.MakeDirContext(nil, "../bad") },
		func() error { return d.DeleteContext(nil, "../bad") },
		func() error { _, err := d.StatContext(nil, "../bad"); return err },
		func() error { _, err := d.ExistsContext(nil, "../bad"); return err },
		func() error { _, err := d.ListContext(nil, "../bad"); return err },
		func() error { _, err := d.ListPageContext(nil, "../bad", 0, 1); return err },
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

// TestRedisIndexEncodingEdges verifies malformed persisted index values fail
// safely and every released and versioned encoding remains distinguishable.
func TestRedisIndexEncodingEdges(t *testing.T) {
	d := &driver{namespace: "ns", prefix: "sandbox"}
	if d.stripPrefix("sandbox") != "" || d.stripPrefix("sandbox/file") != "file" {
		t.Fatal("stripPrefix returned unexpected values")
	}
	if got := (&driver{}).stripPrefix("plain"); got != "plain" {
		t.Fatalf("unprefixed stripPrefix = %q", got)
	}
	if parentDir("") != "" || parentDir("file") != "" || parentDir("a/b") != "a" {
		t.Fatal("parentDir returned unexpected values")
	}
	if objectDirs("") != nil || objectDirs("file") != nil || !slices.Equal(objectDirs("a/b/c"), []string{"a", "a/b"}) {
		t.Fatalf("objectDirs returned unexpected values")
	}
	for _, child := range []string{encodeFileChild("a"), encodeDirChild("b")} {
		if _, _, err := parseChildEntry(child); err != nil {
			t.Fatalf("parseChildEntry(%q): %v", child, err)
		}
	}
	if _, _, err := parseChildEntry("bad"); err == nil {
		t.Fatal("parseChildEntry malformed returned nil error")
	}

	for _, member := range []string{objectMember("a"), dirMarkerMember("b"), legacyDirMarkerMember("c"), "plain"} {
		if _, _, err := parseIndexedMember(member); err != nil {
			t.Fatalf("parseIndexedMember(%q): %v", member, err)
		}
	}
	for _, member := range []string{"storage:v1:object:%", "storage:v1:directory:%"} {
		if _, _, err := parseIndexedMember(member); err == nil {
			t.Fatalf("parseIndexedMember(%q) returned nil error", member)
		}
	}
	for _, key := range []string{"storage:v1:object:x", "storage:v1:directory:x", "dirmarker:x"} {
		if _, ok := unambiguousLegacyObjectMember(key); ok {
			t.Fatalf("unambiguousLegacyObjectMember(%q) = true", key)
		}
	}
	if got, ok := unambiguousLegacyObjectMember("ordinary"); !ok || got != "ordinary" {
		t.Fatalf("unambiguousLegacyObjectMember ordinary = %q, %v", got, ok)
	}

	keys := d.indexedMemberWatchKeys([]indexedMemberRecord{
		{member: "legacy", schema: legacyIndex},
		{member: objectMember("object"), schema: versionedIndex},
		{member: dirMarkerMember("dir"), schema: versionedIndex},
		{member: legacyDirMarkerMember("old-dir"), schema: legacyIndex},
		{member: "storage:v1:object:%", schema: versionedIndex},
	})
	if len(keys) == 0 || !slices.IsSorted(keys) {
		t.Fatalf("indexedMemberWatchKeys = %v", keys)
	}
	if _, err := d.listEntries(context.Background(), []string{"invalid"}); err == nil {
		t.Fatal("listEntries malformed child returned nil error")
	}
}
