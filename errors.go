package storage

import "fmt"

func errUnknownDriver(name string) error {
	return fmt.Errorf("storage: unknown driver %q", name)
}
