package rclone

import (
	"fmt"
	"strings"
)

// LocalRemote defines a local backend configuration.
type LocalRemote struct {
	Name string
}

// RenderLocal returns ini-formatted rclone config for a local backend.
// @group Config
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
func MustRenderLocal(remote LocalRemote) string {
	s, err := RenderLocal(remote)
	if err != nil {
		panic(err)
	}
	return s
}
