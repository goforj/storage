// Command local_build demonstrates direct construction of a local storage disk.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/goforj/storage/driver/localstorage"
)

// main constructs an isolated local disk and demonstrates a write/read round trip.
func main() {
	root, err := os.MkdirTemp("", "storage-local-build-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(root)

	disk, err := localstorage.New(localstorage.Config{
		Root:   root,
		Prefix: "scratch",
	})
	if err != nil {
		log.Fatalf("build disk: %v", err)
	}

	if err := disk.Put("hello.txt", []byte("hello from Build")); err != nil {
		log.Fatalf("put: %v", err)
	}

	data, err := disk.Get("hello.txt")
	if err != nil {
		log.Fatalf("get: %v", err)
	}

	fmt.Printf("read back: %s\n", data)
}
