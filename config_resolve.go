package storage

import (
	"fmt"
	"reflect"
)

// resolveDriverConfig validates and normalizes a typed driver configuration.
func resolveDriverConfig(cfg DriverConfig) (string, ResolvedConfig, error) {
	if isNil(cfg) {
		return "", ResolvedConfig{}, fmt.Errorf("storage: driver config is required")
	}

	name := cfg.DriverName()
	if name == "" {
		return "", ResolvedConfig{}, fmt.Errorf("storage: driver name is required")
	}

	resolved := cfg.ResolvedConfig()
	if resolved.Driver == "" {
		resolved.Driver = name
	}
	if resolved.Driver != name {
		return "", ResolvedConfig{}, fmt.Errorf("storage: driver config mismatch: %q != %q", resolved.Driver, name)
	}

	return name, resolved, nil
}

// isNil reports whether an interface is nil or contains a typed nil value.
func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
