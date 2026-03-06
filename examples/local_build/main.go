package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/goforj/storage/driver/localstorage"
)

func main() {
	root, err := os.MkdirTemp("", "storage-local-build-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(root)

	disk, err := localstorage.New(context.Background(), localstorage.Config{
		Remote: root,
		Prefix: "scratch",
	})
	if err != nil {
		log.Fatalf("build disk: %v", err)
	}

	if err := disk.Put(context.Background(), "hello.txt", []byte("hello from Build")); err != nil {
		log.Fatalf("put: %v", err)
	}

	data, err := disk.Get(context.Background(), "hello.txt")
	if err != nil {
		log.Fatalf("get: %v", err)
	}

	fmt.Printf("read back: %s\n", data)
}
