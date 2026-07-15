// Package sftpstorage provides a host-verified SFTP driver with atomic destination replacement.
// Put and Move require the server's posix-rename extension; the driver returns
// the server error instead of falling back to a non-atomic replacement.
//
// The SSH client dial API cannot be interrupted by context cancellation once
// dialing begins. NewContext checks cancellation before dialing and bounds the
// dial with the configured SSH timeout; streaming operations check cancellation
// between reads and writes.
package sftpstorage
