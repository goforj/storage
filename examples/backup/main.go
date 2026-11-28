// Command backup demonstrates the same backup job on a local disk and RustFS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
	"github.com/goforj/storage/driver/s3storage"
)

// main selects the disk and gives the demo a deadline for bucket setup and storage operations.
func main() {
	backend := flag.String("backend", "local", "backup backend (local or rustfs)")
	root := flag.String("root", "./tmp/backup-demo", "local backup directory")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := run(ctx, *backend, *root); err != nil {
		log.Fatal(err)
	}
}

// run wires the named disk so the backup job receives only a storage handle.
func run(ctx context.Context, backend, root string) (err error) {
	var cfg storage.DriverConfig = localstorage.Config{Root: root}
	switch backend {
	case "local":
	case "rustfs":
		s3 := s3storage.Config{
			Bucket:          "storage-demo",
			Region:          "us-east-1",
			Endpoint:        "http://127.0.0.1:19000",
			AccessKeyID:     "storage-demo",
			SecretAccessKey: "storage-demo-password",
			UsePathStyle:    true,
		}
		if err := s3storage.PrepareBucketContext(ctx, s3); err != nil {
			return err
		}
		cfg = s3
	default:
		return fmt.Errorf("unknown backend %q, choose local or rustfs", backend)
	}

	manager, err := storage.New(storage.Config{
		Default: "backups",
		Disks:   map[storage.DiskName]storage.DriverConfig{"backups": cfg},
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, manager.Close()) }()

	disk, err := manager.Disk("backups")
	if err != nil {
		return err
	}
	return backup(disk.WithContext(ctx))
}

// backup writes a sample snapshot, lists available snapshots, and restores the same bytes.
func backup(disk storage.Storage) error {
	const key = "snapshots/config.json"
	const snapshot = `{"site":"example","version":1}`
	if err := disk.Put(key, []byte(snapshot)); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	fmt.Println("Saved:", key)

	entries, err := disk.List("snapshots")
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	for _, entry := range entries {
		fmt.Println("Found:", entry.Path)
	}

	restored, err := disk.Get(key)
	if err != nil {
		return fmt.Errorf("restore snapshot: %w", err)
	}
	if string(restored) != snapshot {
		return errors.New("restored content differs from the snapshot")
	}
	fmt.Println("Restored:", string(restored))
	return nil
}
