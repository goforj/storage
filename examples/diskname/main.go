//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage"
)

func main() {
	// DiskName is a typed identifier for configured disks.

	// Example: declare a disk name
	const uploads storage.DiskName = "uploads"
	fmt.Println(uploads)
	// Output: uploads
}
