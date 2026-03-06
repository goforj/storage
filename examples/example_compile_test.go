package examples

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExamplesBuild(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("cannot read examples directory: %v", err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		name := e.Name()
		path := filepath.Join(".", name)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := buildExampleWithoutTags(path); err != nil {
				t.Fatalf("example %q failed to build:\n%s", name, err)
			}
		})
	}
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return a
}

func buildExampleWithoutTags(exampleDir string) error {
	orig := filepath.Join(exampleDir, "main.go")

	src, err := os.ReadFile(orig)
	if err != nil {
		return fmt.Errorf("read main.go: %w", err)
	}

	clean := stripBuildTags(src)

	tmpDir, err := os.MkdirTemp("", "example-overlay-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	tmpFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(tmpFile, clean, 0o644); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(exampleBuildGoMod()), 0o644); err != nil {
		return err
	}

	overlay := map[string]any{
		"Replace": map[string]string{
			abs(orig): abs(tmpFile),
		},
	}

	overlayJSON, err := json.Marshal(overlay)
	if err != nil {
		return err
	}

	overlayPath := filepath.Join(tmpDir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlayJSON, 0o644); err != nil {
		return err
	}

	cmd := exec.Command(
		"go", "build",
		"-mod=mod",
		"-overlay", overlayPath,
		"-o", os.DevNull,
		".",
	)
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOSUMDB=off")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return errors.New(stderr.String())
	}

	return nil
}

func exampleBuildGoMod() string {
	root := abs("..")
	sep := string(filepath.Separator)
	rootSlash := filepath.ToSlash(root)
	if runtime.GOOS == "windows" {
		rootSlash = strings.ReplaceAll(root, sep, "/")
	}
	lines := []string{
		"module examplebuild",
		"",
		"go 1.24.4",
		"",
		"require (",
		"\tgithub.com/goforj/storage v0.0.0",
		"\tgithub.com/goforj/storage/driver/localstorage v0.0.0",
		"\tgithub.com/goforj/storage/driver/s3storage v0.0.0",
		"\tgithub.com/goforj/storage/driver/gcsstorage v0.0.0",
		"\tgithub.com/goforj/storage/driver/sftpstorage v0.0.0",
		"\tgithub.com/goforj/storage/driver/ftpstorage v0.0.0",
		"\tgithub.com/goforj/storage/driver/dropboxstorage v0.0.0",
		"\tgithub.com/goforj/storage/driver/rclonestorage v0.0.0",
		")",
		"",
		"replace github.com/goforj/storage => " + rootSlash,
		"replace github.com/goforj/storage/driver/localstorage => " + rootSlash + "/driver/localstorage",
		"replace github.com/goforj/storage/driver/s3storage => " + rootSlash + "/driver/s3storage",
		"replace github.com/goforj/storage/driver/gcsstorage => " + rootSlash + "/driver/gcsstorage",
		"replace github.com/goforj/storage/driver/sftpstorage => " + rootSlash + "/driver/sftpstorage",
		"replace github.com/goforj/storage/driver/ftpstorage => " + rootSlash + "/driver/ftpstorage",
		"replace github.com/goforj/storage/driver/dropboxstorage => " + rootSlash + "/driver/dropboxstorage",
		"replace github.com/goforj/storage/driver/rclonestorage => " + rootSlash + "/driver/rclonestorage",
		"",
	}
	return strings.Join(lines, "\n")
}

func stripBuildTags(src []byte) []byte {
	lines := strings.Split(string(src), "\n")

	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])

		if strings.HasPrefix(line, "//go:build") ||
			strings.HasPrefix(line, "// +build") ||
			line == "" {
			i++
			continue
		}

		break
	}

	return []byte(strings.Join(lines[i:], "\n"))
}
