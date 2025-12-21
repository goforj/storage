package filesystem

import "context"

// FSWithHelpers wraps a Filesystem and provides convenience methods
// that use context.Background(). This keeps the core interface strictly
// context-aware while offering opt-in ergonomics for callers that do
// not need explicit contexts.
type FSWithHelpers struct {
	FS Filesystem
}

// WithHelpers returns a wrapper that exposes non-context convenience
// methods backed by the provided Filesystem.
func WithHelpers(fs Filesystem) FSWithHelpers {
	return FSWithHelpers{FS: fs}
}

func (w FSWithHelpers) Get(p string) ([]byte, error) {
	return w.FS.Get(context.Background(), p)
}

func (w FSWithHelpers) Put(p string, contents []byte) error {
	return w.FS.Put(context.Background(), p, contents)
}

func (w FSWithHelpers) Delete(p string) error {
	return w.FS.Delete(context.Background(), p)
}

func (w FSWithHelpers) Exists(p string) (bool, error) {
	return w.FS.Exists(context.Background(), p)
}

func (w FSWithHelpers) List(p string) ([]Entry, error) {
	return w.FS.List(context.Background(), p)
}

func (w FSWithHelpers) URL(p string) (string, error) {
	return w.FS.URL(context.Background(), p)
}
