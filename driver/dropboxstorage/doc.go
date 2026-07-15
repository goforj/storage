// Package dropboxstorage provides a Dropbox-backed storagecore driver.
//
// Directory deletion returns storagecore.ErrUnsupported. Dropbox's DeleteV2
// operation is recursive, so even an empty-folder preflight could delete a child
// created between the check and deletion. File deletion remains supported and
// uses ParentRev so a changed path cannot be recursively deleted by mistake.
//
// The Dropbox SDK methods used here do not accept context.Context and therefore
// cannot be interrupted while an RPC is in flight. Context-aware operations
// check cancellation before and after SDK calls, and upload/download bodies also
// observe cancellation while streaming.
package dropboxstorage
