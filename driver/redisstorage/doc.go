// Package redisstorage provides transactionally indexed Redis object storage.
//
// Index migration requires a coordinated cutover. This version reads indexes
// written by v0.4.6 and earlier, but new writes use collision-free v2 indexes
// that older clients cannot read or validate. Stop every storage client sharing
// the Redis database and prefix before upgrading them together; mixed-version
// operation is not a supported steady state.
package redisstorage
