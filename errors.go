package storage

import "fmt"

// errUnknownDriver formats registry misses consistently across Build and Manager.
func errUnknownDriver(name string) error {
	return fmt.Errorf("storage: unknown driver %q", name)
}
