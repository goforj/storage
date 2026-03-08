//go:build integration

package redisstorage

import (
	"context"
	"testing"
	"time"

	"github.com/goforj/storage/storagetest"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestRedisStorageContract(t *testing.T) {
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

	storagetest.RunStorageContractTests(t, store)
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
