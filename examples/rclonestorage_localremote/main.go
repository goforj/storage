//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/storage/driver/rclonestorage"
)

func main() {
	// LocalRemote defines a local backend configuration.

	// Example: define a local remote
	remote := rclonestorage.LocalRemote{Name: "local"}
	fmt.Println(remote.Name)
	// Output: local
}
