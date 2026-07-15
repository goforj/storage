// Package rclonestorage adapts independently constructed rclone backends to the shared storagecore contract.
// Closing a driver invokes that backend's optional Shutdown feature with a
// bounded context so cached connections and background workers are released.
package rclonestorage
