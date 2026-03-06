package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	assetsRoot, err := os.MkdirTemp("", "storage-assets-*")
	if err != nil {
		log.Fatalf("assets temp dir: %v", err)
	}
	defer os.RemoveAll(assetsRoot)

	uploadsRoot, err := os.MkdirTemp("", "storage-uploads-*")
	if err != nil {
		log.Fatalf("uploads temp dir: %v", err)
	}
	defer os.RemoveAll(uploadsRoot)

	mgr, err := storage.New(storage.Config{
		Default: "assets",
		Disks: map[storage.DiskName]storage.DriverConfig{
			"assets":  localstorage.Config{Remote: assetsRoot, Prefix: "assets"},
			"uploads": localstorage.Config{Remote: uploadsRoot, Prefix: "uploads"},
		},
	})
	if err != nil {
		log.Fatalf("manager: %v", err)
	}

	assets := mgr.Default()
	uploads, err := mgr.Disk("uploads")
	if err != nil {
		log.Fatalf("uploads disk: %v", err)
	}

	if err := assets.Put(context.Background(), "logo.txt", []byte("asset content")); err != nil {
		log.Fatalf("assets put: %v", err)
	}
	if err := uploads.Put(context.Background(), "avatar.txt", []byte("upload content")); err != nil {
		log.Fatalf("uploads put: %v", err)
	}

	assetData, err := assets.Get(context.Background(), "logo.txt")
	if err != nil {
		log.Fatalf("assets get: %v", err)
	}
	uploadData, err := uploads.Get(context.Background(), "avatar.txt")
	if err != nil {
		log.Fatalf("uploads get: %v", err)
	}

	fmt.Printf("assets: %s\n", assetData)
	fmt.Printf("uploads: %s\n", uploadData)
}
