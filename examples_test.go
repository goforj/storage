package filesystem

import (
	"context"
	"fmt"
	"log"
)

// ExampleNormalizePath shows path cleaning and traversal rejection.
func ExampleNormalizePath() {
	p, err := NormalizePath("/folder/../file.txt")
	fmt.Println(p, err == nil)

	_, err = NormalizePath("../escape")
	fmt.Println("isForbidden", err != nil)
	// Output:
	// file.txt true
	// isForbidden true
}

// ExampleJoinPrefix demonstrates joining prefixes safely.
func ExampleJoinPrefix() {
	fmt.Println(JoinPrefix("base", "file.txt"))
	fmt.Println(JoinPrefix("base/sub", "nested/file.txt"))
	// Output:
	// base/file.txt
	// base/sub/nested/file.txt
}

// ExampleManager_Disk shows constructing a manager and selecting a disk.
func ExampleManager_Disk() {
	RegisterDriver("stub", func(context.Context, DiskConfig, Config) (Filesystem, error) {
		return noopFS{}, nil
	})

	cfg := Config{
		Default: "primary",
		Disks: map[DiskName]DiskConfig{
			"primary": {Driver: "stub"},
		},
	}
	mgr, err := New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	_, err = mgr.Disk("primary")
	fmt.Println(err == nil)
	// Output:
	// true
}

// noopFS is a trivial Filesystem used only in examples.
type noopFS struct{}

func (noopFS) Get(context.Context, string) ([]byte, error)   { return nil, nil }
func (noopFS) Put(context.Context, string, []byte) error     { return nil }
func (noopFS) Delete(context.Context, string) error          { return nil }
func (noopFS) Exists(context.Context, string) (bool, error)  { return false, nil }
func (noopFS) List(context.Context, string) ([]Entry, error) { return nil, nil }
func (noopFS) URL(context.Context, string) (string, error)   { return "", ErrUnsupported }
