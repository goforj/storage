//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage"
)

func main() {
	// JoinPrefix combines a disk prefix with a path using slash separators.

	// Example: join a disk prefix and path
	fmt.Println(storage.JoinPrefix("assets", "logo.svg"))
	// Output: assets/logo.svg
}
