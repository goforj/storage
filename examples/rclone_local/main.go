package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/goforj/storage"
	rclonedriver "github.com/goforj/storage/driver/rclone"
)

const inlineConfig = `
[localfs]
type = local
`

func main() {
	// Create a temp directory to act as the local backend root.
	root, err := os.MkdirTemp("", "rclone-local-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(root)

	cfg := storage.Config{
		Default: "rclone",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"rclone": rclonedriver.Config{
				Remote:           fmt.Sprintf("localfs:%s", root),
				Prefix:           "sandbox",
				RcloneConfigData: inlineConfig,
			},
		},
	}

	ctx := context.Background()

	mgr, err := storage.New(cfg)
	if err != nil {
		log.Fatalf("manager: %v", err)
	}
	fs, err := mgr.Disk("rclone")
	if err != nil {
		log.Fatalf("disk: %v", err)
	}

	// Use helper wrapper for non-context convenience calls.

	if err := fs.Put(ctx, "folder/file.txt", []byte("hello rclone local")); err != nil {
		log.Fatalf("put: %v", err)
	}
	data, err := fs.Get(ctx, "folder/file.txt")
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	fmt.Printf("read back: %s\n", data)
}
