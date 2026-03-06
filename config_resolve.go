package storage

import "fmt"

func resolveDriverConfig(cfg DriverConfig) (string, ResolvedConfig, error) {
	if cfg == nil {
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
