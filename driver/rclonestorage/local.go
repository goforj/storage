package rclonestorage

import (
	"fmt"
	"strings"
)

// LocalRemote defines a local backend configuration.
// @group Config
//
// Example: define a local remote
//
//	remote := rclonestorage.LocalRemote{Name: "local"}
//	fmt.Println(remote.Name)
//	// Output: local
//
// Example: define a local remote with all fields
//
//	remote := rclonestorage.LocalRemote{
//		Name: "local",
//	}
//	fmt.Println(remote.Name)
//	// Output: local
type LocalRemote struct {
	Name string
}

// RenderLocal returns ini-formatted rclone config for a local backend.
// @group Config
//
// Example: render a local remote
//
//	cfg, _ := rclonestorage.RenderLocal(rclonestorage.LocalRemote{Name: "local"})
//	fmt.Println(cfg)
//	// Output:
//	// [local]
//	// type = local
func RenderLocal(remote LocalRemote) (string, error) {
	if remote.Name == "" {
		return "", fmt.Errorf("rclone: remote name is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", remote.Name)
	fmt.Fprintf(&b, "type = local\n")
	return b.String(), nil
}

// MustRenderLocal panics on error.
// @group Config
//
// Example: render a local remote without handling the error
//
//	cfg := rclonestorage.MustRenderLocal(rclonestorage.LocalRemote{Name: "local"})
//	fmt.Println(cfg)
//	// Output:
//	// [local]
//	// type = local
func MustRenderLocal(remote LocalRemote) string {
	s, err := RenderLocal(remote)
	if err != nil {
		panic(err)
	}
	return s
}
