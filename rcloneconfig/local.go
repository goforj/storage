package rcloneconfig

import (
	"fmt"
	"strings"
)

// LocalRemote defines a local backend configuration.
type LocalRemote struct {
	Name string
}

// RenderLocal returns ini-formatted rclone config for a local backend.
func RenderLocal(remote LocalRemote) (string, error) {
	if remote.Name == "" {
		return "", fmt.Errorf("rcloneconfig: remote name is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", remote.Name)
	fmt.Fprintf(&b, "type = local\n")
	return b.String(), nil
}

// MustRenderLocal panics on error.
func MustRenderLocal(name string) string {
	s, err := RenderLocal(LocalRemote{Name: name})
	if err != nil {
		panic(err)
	}
	return s
}
