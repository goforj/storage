//go:build integration

package all

import (
	"os"
	"strings"
)

func selectedIntegrationDrivers() map[string]bool {
	selected := map[string]bool{
		"ftp":          true,
		"gcs":          true,
		"local":        true,
		"memory":       true,
		"rclone_local": true,
		"s3":           true,
		"sftp":         true,
	}

	value := strings.TrimSpace(strings.ToLower(os.Getenv("INTEGRATION_DRIVER")))
	if value == "" || value == "all" {
		return selected
	}

	for key := range selected {
		selected[key] = false
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		selected[part] = true
	}
	return selected
}

func integrationDriverEnabled(name string) bool {
	return selectedIntegrationDrivers()[strings.ToLower(name)]
}
