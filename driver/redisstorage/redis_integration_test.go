//go:build integration

package redisstorage

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/goforj/storage/storagecore"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestRedisStorageBuildAndIO(t *testing.T) {
	ctx := context.Background()
	container, addr := startRedisContainer(t, ctx)
	t.Cleanup(func() {
		if err := shutdownContainer(container); err != nil {
			t.Fatalf("terminate redis container: %v", err)
		}
	})

	store, err := New(Config{
		Addr:   addr,
		Prefix: "itest",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := store.(*driver).Close(); err != nil {
			t.Fatalf("close redis client: %v", err)
		}
	})

	if err := store.Put("hello.txt", []byte("redis")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get("hello.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "redis" {
		t.Fatalf("Get = %q", got)
	}
}

func TestRedisIndexesReflectHierarchy(t *testing.T) {
	d := newIntegrationDriver(t)
	ctx := context.Background()

	if err := d.Put("nested/leaf/file.txt", []byte("ok")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	assertSetMembers(t, d, ctx, d.dirChildrenKey(""), []string{encodeDirChild("itest/nested")})
	assertSetMembers(t, d, ctx, d.dirChildrenKey("itest/nested"), []string{encodeDirChild("itest/nested/leaf")})
	assertSetMembers(t, d, ctx, d.dirChildrenKey("itest/nested/leaf"), []string{encodeFileChild("itest/nested/leaf/file.txt")})

	assertSetMembers(t, d, ctx, d.dirObjectsKey(""), []string{"itest/nested/leaf/file.txt"})
	assertSetMembers(t, d, ctx, d.dirObjectsKey("itest/nested"), []string{"itest/nested/leaf/file.txt"})
	assertSetMembers(t, d, ctx, d.dirObjectsKey("itest/nested/leaf"), []string{"itest/nested/leaf/file.txt"})
}

func TestRedisDeletePrunesEmptyDirectories(t *testing.T) {
	d := newIntegrationDriver(t)
	ctx := context.Background()

	if err := d.Put("nested/leaf/file.txt", []byte("ok")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := d.Delete("nested/leaf/file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	assertSetMembers(t, d, ctx, d.dirObjectsKey(""), nil)
	assertKeyMissing(t, d, ctx, d.dirObjectsKey("itest/nested"))
	assertKeyMissing(t, d, ctx, d.dirObjectsKey("itest/nested/leaf"))
	assertKeyMissing(t, d, ctx, d.dirChildrenKey("itest/nested"))
	assertKeyMissing(t, d, ctx, d.dirChildrenKey("itest/nested/leaf"))
	assertSetMembers(t, d, ctx, d.dirChildrenKey(""), nil)
}

func TestRedisMoveReindexesDirectories(t *testing.T) {
	d := newIntegrationDriver(t)
	ctx := context.Background()

	if err := d.Put("from/leaf/file.txt", []byte("ok")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := d.Move("from/leaf/file.txt", "to/branch/file.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	assertKeyMissing(t, d, ctx, d.objectKey("itest/from/leaf/file.txt"))
	assertSetMembers(t, d, ctx, d.dirObjectsKey(""), []string{"itest/to/branch/file.txt"})
	assertSetMembers(t, d, ctx, d.dirChildrenKey(""), []string{encodeDirChild("itest/to")})
	assertKeyMissing(t, d, ctx, d.dirObjectsKey("itest/from"))
	assertSetMembers(t, d, ctx, d.dirChildrenKey("itest/to"), []string{encodeDirChild("itest/to/branch")})
	assertSetMembers(t, d, ctx, d.dirChildrenKey("itest/to/branch"), []string{encodeFileChild("itest/to/branch/file.txt")})
}

func TestRedisListAndWalkUseIndexedHierarchy(t *testing.T) {
	d := newIntegrationDriver(t)

	files := []string{
		"one/file-a.txt",
		"one/two/file-b.txt",
		"three/file-c.txt",
	}
	for _, file := range files {
		if err := d.Put(file, []byte(file)); err != nil {
			t.Fatalf("Put %q: %v", file, err)
		}
	}

	rootEntries, err := d.List("")
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	assertPaths(t, rootEntries, []string{"one", "three"})

	oneEntries, err := d.List("one")
	if err != nil {
		t.Fatalf("List one: %v", err)
	}
	assertPaths(t, oneEntries, []string{"one/file-a.txt", "one/two"})

	var walked []string
	if err := d.Walk("", func(entry storagecore.Entry) error {
		walked = append(walked, entry.Path)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	slices.Sort(walked)
	wantWalk := []string{"one", "one/file-a.txt", "one/two", "one/two/file-b.txt", "three", "three/file-c.txt"}
	if !slices.Equal(walked, wantWalk) {
		t.Fatalf("Walk paths = %v want %v", walked, wantWalk)
	}
}

func newIntegrationDriver(t *testing.T) *driver {
	t.Helper()
	ctx := context.Background()
	container, addr := startRedisContainer(t, ctx)
	t.Cleanup(func() {
		if err := shutdownContainer(container); err != nil {
			t.Fatalf("terminate redis container: %v", err)
		}
	})

	store, err := New(Config{
		Addr:   addr,
		Prefix: "itest",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d, ok := store.(*driver)
	if !ok {
		t.Fatalf("store type = %T", store)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("close redis client: %v", err)
		}
	})
	return d
}

func assertSetMembers(t *testing.T, d *driver, ctx context.Context, key string, want []string) {
	t.Helper()
	got, err := d.client.SMembers(ctx, key).Result()
	if err != nil {
		t.Fatalf("SMembers(%q): %v", key, err)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("SMembers(%q) = %v want %v", key, got, want)
	}
}

func assertKeyMissing(t *testing.T, d *driver, ctx context.Context, key string) {
	t.Helper()
	exists, err := d.client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Exists(%q): %v", key, err)
	}
	if exists != 0 {
		t.Fatalf("Exists(%q) = %d want 0", key, exists)
	}
}

func assertPaths(t *testing.T, entries []storagecore.Entry, want []string) {
	t.Helper()
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Path)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("paths = %v want %v", got, want)
	}
}

func startRedisContainer(t *testing.T, ctx context.Context) (testcontainers.Container, string) {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = shutdownContainer(container)
		t.Fatalf("redis host: %v", err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		_ = shutdownContainer(container)
		t.Fatalf("redis mapped port: %v", err)
	}
	return container, host + ":" + port.Port()
}

func shutdownContainer(container testcontainers.Container) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return container.Terminate(ctx)
}
