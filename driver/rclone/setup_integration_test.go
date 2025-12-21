//go:build integration

package rclone

import (
	"sync"
)

var (
	rcloneInitOnce sync.Once
)

// setRcloneConfigData initializes the global rclone config once; subsequent calls return false if different data is passed.
func setRcloneConfigData(data string) (applied bool) {
	initConfigData = data
	return true
}
