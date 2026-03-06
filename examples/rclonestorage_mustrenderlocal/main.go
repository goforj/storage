//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage/driver/rclonestorage"
)

func main() {
	// MustRenderLocal panics on error.

	// Example: render a local remote without handling the error
	cfg := rclonestorage.MustRenderLocal(rclonestorage.LocalRemote{Name: "local"})
	fmt.Println(cfg)
	// Output:
	// [local]
	// type = local
}
