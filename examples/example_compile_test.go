package examples

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExamplesBuild(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("cannot read examples directory: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(".", name)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := buildExample(path); err != nil {
				t.Fatalf("example %q failed to build:\n%s", name, err)
			}
		})
	}
}

func buildExample(exampleDir string) error {
	cmd := exec.Command(
		"go", "build",
		"-o", os.DevNull,
		".",
	)
	cmd.Dir = exampleDir
	cmd.Env = append(os.Environ(), "GOWORK=off")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return errors.New(stderr.String())
	}

	return nil
}
