package redisstorage

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/storage"
)

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

func TestNewRequiresAddr(t *testing.T) {
	_, err := New(Config{})
	if err == nil || err.Error() != "storage: redis storage requires RedisAddr" {
		t.Fatalf("New error = %v", err)
	}
}

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
	if err := store.WalkContext(ctx, "", func(storage.Entry) error { return nil }); !errors.Is(err, context.Canceled) {
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

func TestRedisNamespace(t *testing.T) {
	if got := redisNamespace(storage.ResolvedConfig{}); got != "goforj:storage:redis" {
		t.Fatalf("redisNamespace default = %q", got)
	}
	if got := redisNamespace(storage.ResolvedConfig{RedisDB: 3}); got != "goforj:storage:redis:db:3" {
		t.Fatalf("redisNamespace db = %q", got)
	}
}
