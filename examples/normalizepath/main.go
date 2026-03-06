//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage"
)

func main() {
	// NormalizePath cleans a user path, normalizes separators, and rejects attempts
	// to escape the disk root or prefix root.
	//
	// The empty string and root-like inputs normalize to the logical root.

	// Example: normalize a user path
	p, _ := storage.NormalizePath(" /avatars//user-1.png ")
	fmt.Println(p)
	// Output: avatars/user-1.png
}
