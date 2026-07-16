// Command rclone_local demonstrates rclone storage backed by a local remote.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/rclonestorage"
)

const inlineConfig = `
[localfs]
type = local
`

// main builds an inline local rclone remote and demonstrates a write/read round trip.
func main() {
	// A temporary backend keeps the runnable example from leaving files behind.
	root, err := os.MkdirTemp("", "rclone-local-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(root)

	cfg := storage.Config{
		Default: "rclone",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"rclone": rclonestorage.Config{
				Remote:           fmt.Sprintf("localfs:%s", root),
				Prefix:           "sandbox",
				RcloneConfigData: inlineConfig,
			},
		},
	}

	mgr, err := storage.New(cfg)
	if err != nil {
		log.Fatalf("manager: %v", err)
	}
	fs, err := mgr.Disk("rclone")
	if err != nil {
		log.Fatalf("disk: %v", err)
	}

	if err := fs.Put("folder/file.txt", []byte("hello rclone local")); err != nil {
		log.Fatalf("put: %v", err)
	}
	data, err := fs.Get("folder/file.txt")
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	fmt.Printf("read back: %s\n", data)
}
