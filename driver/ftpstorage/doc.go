// Package ftpstorage provides a serialized FTP storagecore driver with optional verified TLS.
// Disabling certificate verification is rejected unless TLS is enabled, and
// verified TLS always uses the configured host name with a TLS 1.2 minimum.
//
// The underlying FTP client cannot interrupt an in-flight command or response
// read through context.Context. Context-aware methods check cancellation before
// commands and between bounded stream reads, so cancellation is best effort once
// a server command has started.
package ftpstorage
