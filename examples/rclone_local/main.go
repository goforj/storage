package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/goforj/filesystem"
	_ "github.com/goforj/filesystem/driver/local"
	_ "github.com/goforj/filesystem-rclone/driver/rclone"
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

	cfg := filesystem.Config{
		Default:          "rclone",
		RcloneConfigData: inlineConfig,
		Disks: map[filesystem.DiskName]filesystem.DiskConfig{
			"rclone": {
				Driver: "rclone",
				Remote: fmt.Sprintf("localfs:%s", root),
				Prefix: "sandbox",
			},
		},
	}

	mgr, err := filesystem.New(cfg)
	if err != nil {
		log.Fatalf("manager: %v", err)
	}
	fs, err := mgr.Disk("rclone")
	if err != nil {
		log.Fatalf("disk: %v", err)
	}

	ctx := context.Background()
	if err := fs.Put(ctx, "folder/file.txt", []byte("hello rclone local")); err != nil {
		log.Fatalf("put: %v", err)
	}
	data, err := fs.Get(ctx, "folder/file.txt")
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	fmt.Printf("read back: %s\n", data)
}
