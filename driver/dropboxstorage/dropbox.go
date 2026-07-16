package dropboxstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/goforj/storage/storagecore"
)

// init registers the Dropbox driver with storagecore's runtime registry.
func init() {
	storagecore.RegisterDriver("dropbox", func(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
		return newFromDiskConfig(ctx, cfg)
	})
}

type driver struct {
	client dropboxClient
	prefix string
}

type dropboxClient interface {
	Download(*files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error)
	Upload(*files.UploadArg, io.Reader) (*files.FileMetadata, error)
	DeleteV2(*files.DeleteArg) (*files.DeleteResult, error)
	CreateFolderV2(*files.CreateFolderArg) (*files.CreateFolderResult, error)
	MoveV2(*files.RelocationArg) (*files.RelocationResult, error)
	GetMetadata(*files.GetMetadataArg) (files.IsMetadata, error)
	ListFolder(*files.ListFolderArg) (*files.ListFolderResult, error)
	ListFolderContinue(*files.ListFolderContinueArg) (*files.ListFolderResult, error)
	GetTemporaryLink(*files.GetTemporaryLinkArg) (*files.GetTemporaryLinkResult, error)
}

// Config defines a Dropbox-backed storage disk.
// @group Driver Config
//
// Example: define dropbox storage config
//
//	cfg := dropboxstorage.Config{
//		Token: "token",
//	}
//	_ = cfg
//
// Example: define dropbox storage config with all fields
//
//	cfg := dropboxstorage.Config{
//		Token:  "token",
//		Prefix: "uploads", // default: ""
//	}
//	_ = cfg
type Config struct {
	Token  string
	Prefix string
}

// DriverName returns the registry name for Dropbox storage.
func (Config) DriverName() string { return "dropbox" }

// ResolvedConfig translates Config into the shared driver boundary.
func (c Config) ResolvedConfig() storagecore.ResolvedConfig {
	return storagecore.ResolvedConfig{
		Driver:       "dropbox",
		DropboxToken: c.Token,
		Prefix:       c.Prefix,
	}
}

// New constructs Dropbox-backed storage using the official SDK.
// @group Driver Constructors
//
// Example: dropbox storage
//
//	fs, _ := dropboxstorage.New(dropboxstorage.Config{
//		Token: "token",
//	})
//	_ = fs
func New(cfg Config) (storagecore.Storage, error) {
	return NewContext(context.Background(), cfg)
}

// NewContext constructs Dropbox-backed storage and honors cancellation during setup.
func NewContext(ctx context.Context, cfg Config) (storagecore.Storage, error) {
	return newFromDiskConfig(ctx, cfg.ResolvedConfig())
}

// newFromDiskConfig validates the logical namespace before constructing the Dropbox SDK client.
func newFromDiskConfig(ctx context.Context, cfg storagecore.ResolvedConfig) (storagecore.Storage, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.DropboxToken == "" {
		return nil, fmt.Errorf("storage: dropbox storage requires DropboxToken")
	}
	prefix, err := storagecore.NormalizePath(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	dbx := files.New(dropbox.Config{
		Token:    cfg.DropboxToken,
		LogLevel: dropbox.LogOff,
	})
	return &driver{client: dbx, prefix: prefix}, nil
}

// Get downloads one Dropbox file without caller cancellation.
func (d *driver) Get(p string) ([]byte, error) {
	return d.GetContext(context.Background(), p)
}

// GetContext reads a file while observing cancellation and always closes the download body.
func (d *driver) GetContext(ctx context.Context, p string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, fmt.Errorf("%w: a file path is required", storagecore.ErrForbidden)
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return nil, err
	}
	_, content, err := d.client.Download(files.NewDownloadArg(full))
	if contextErr := ctx.Err(); contextErr != nil {
		if content == nil {
			return nil, contextErr
		}
		return nil, joinCleanup(contextErr, wrapError(content.Close()))
	}
	if err != nil {
		if content == nil {
			return nil, wrapError(err)
		}
		return nil, joinCleanup(wrapError(err), wrapError(content.Close()))
	}
	var data bytes.Buffer
	_, err = copyContext(ctx, &data, content)
	closeErr := content.Close()
	if err != nil {
		return nil, joinCleanup(wrapError(err), wrapError(closeErr))
	}
	if closeErr != nil {
		return nil, wrapError(closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

// Put replaces one Dropbox file without caller cancellation.
func (d *driver) Put(p string, contents []byte) error {
	return d.PutContext(context.Background(), p, contents)
}

// MakeDir creates a Dropbox folder chain without caller cancellation.
func (d *driver) MakeDir(p string) error {
	return d.MakeDirContext(context.Background(), p)
}

// PutContext replaces a file after recursively ensuring its parent folders exist.
func (d *driver) PutContext(ctx context.Context, p string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	if normalized == "" {
		return fmt.Errorf("%w: a file path is required", storagecore.ErrForbidden)
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return err
	}
	if err := d.ensureParentDirectories(ctx, full); err != nil {
		return err
	}
	arg := files.NewUploadArg(full)
	arg.Mode = &files.WriteMode{Tagged: dropbox.Tagged{Tag: files.WriteModeOverwrite}}
	_, err = d.client.Upload(arg, &contextReader{ctx: ctx, reader: bytes.NewReader(contents)})
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return wrapError(err)
	}
	return ctx.Err()
}

// MakeDirContext recursively creates a directory and treats the logical root as already present.
func (d *driver) MakeDirContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	if normalized == "" {
		return nil
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return err
	}
	return d.ensureDirectory(ctx, full)
}

// Delete removes one Dropbox file without caller cancellation.
func (d *driver) Delete(p string) error {
	return d.DeleteContext(context.Background(), p)
}

// DeleteContext removes files and rejects folders because Dropbox exposes only recursive deletion.
func (d *driver) DeleteContext(ctx context.Context, p string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	if normalized == "" {
		return fmt.Errorf("%w: deleting the Dropbox root is not allowed", storagecore.ErrForbidden)
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return err
	}
	metadata, err := d.client.GetMetadata(files.NewGetMetadataArg(full))
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return wrapError(err)
	}
	file, ok := metadata.(*files.FileMetadata)
	if !ok {
		return fmt.Errorf("%w: Dropbox cannot delete directories without recursively deleting concurrent children", storagecore.ErrUnsupported)
	}
	deleteArg := files.NewDeleteArg(full)
	deleteArg.ParentRev = file.Rev
	_, err = d.client.DeleteV2(deleteArg)
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return wrapError(err)
	}
	return ctx.Err()
}

// Stat inspects one Dropbox path without caller cancellation.
func (d *driver) Stat(p string) (storagecore.Entry, error) {
	return d.StatContext(context.Background(), p)
}

// StatContext reports a display-cased path relative to the configured logical namespace.
func (d *driver) StatContext(ctx context.Context, p string) (storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return storagecore.Entry{}, err
	}
	if normalized == "" {
		return storagecore.Entry{Path: "", IsDir: true}, nil
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return storagecore.Entry{}, err
	}
	meta, err := d.client.GetMetadata(files.NewGetMetadataArg(full))
	if contextErr := ctx.Err(); contextErr != nil {
		return storagecore.Entry{}, contextErr
	}
	if err != nil {
		return storagecore.Entry{}, wrapError(err)
	}
	if err := ctx.Err(); err != nil {
		return storagecore.Entry{}, err
	}
	switch m := meta.(type) {
	case *files.FileMetadata:
		return storagecore.Entry{Path: d.metadataPath(m.Metadata), Size: int64(m.Size), IsDir: false}, nil
	case *files.FolderMetadata:
		return storagecore.Entry{Path: d.metadataPath(m.Metadata), Size: 0, IsDir: true}, nil
	default:
		return storagecore.Entry{}, fmt.Errorf("%w: unsupported metadata type", storagecore.ErrUnsupported)
	}
}

// Exists reports Dropbox file presence without treating folders as objects.
func (d *driver) Exists(p string) (bool, error) {
	return d.ExistsContext(context.Background(), p)
}

// ExistsContext reports only files because directory presence is exposed through Stat.
func (d *driver) ExistsContext(ctx context.Context, p string) (bool, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return false, err
	}
	if normalized == "" {
		return false, nil
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return false, err
	}
	metadata, err := d.client.GetMetadata(files.NewGetMetadataArg(full))
	if contextErr := ctx.Err(); contextErr != nil {
		return false, contextErr
	}
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, wrapError(err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, isFile := metadata.(*files.FileMetadata)
	return isFile, nil
}

// List returns sorted immediate Dropbox children without caller cancellation.
func (d *driver) List(p string) ([]storagecore.Entry, error) {
	return d.ListContext(context.Background(), p)
}

// ListPage paginates sorted immediate Dropbox children without caller cancellation.
func (d *driver) ListPage(p string, offset, limit int) (storagecore.ListPageResult, error) {
	return d.ListPageContext(context.Background(), p, offset, limit)
}

// ListContext returns immediate Dropbox children in deterministic display-path order.
func (d *driver) ListContext(ctx context.Context, p string) ([]storagecore.Entry, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return nil, err
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return nil, err
	}
	arg := files.NewListFolderArg(full)
	arg.Recursive = false

	var entries []storagecore.Entry
	err = d.listPage(ctx, arg, &entries)
	if err != nil {
		if normalized == "" && isNotFound(err) {
			return []storagecore.Entry{}, nil
		}
		return nil, wrapError(err)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

// ListPageContext paginates the driver's deterministic logical listing.
func (d *driver) ListPageContext(ctx context.Context, p string, offset, limit int) (storagecore.ListPageResult, error) {
	entries, err := d.ListContext(ctx, p)
	if err != nil {
		return storagecore.ListPageResult{}, err
	}
	return storagecore.PaginateEntries(entries, offset, limit), nil
}

// listPage appends an initial Dropbox page and drains every continuation iteratively.
func (d *driver) listPage(ctx context.Context, arg *files.ListFolderArg, entries *[]storagecore.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := d.client.ListFolder(arg)
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.appendEntries(ctx, res.Entries, entries); err != nil {
		return err
	}
	if res.HasMore {
		continueArg := files.NewListFolderContinueArg(res.Cursor)
		return d.listContinue(ctx, continueArg, entries)
	}
	return nil
}

// listContinue iteratively follows cursors so large listings do not grow the call stack.
func (d *driver) listContinue(ctx context.Context, arg *files.ListFolderContinueArg, entries *[]storagecore.Entry) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := d.client.ListFolderContinue(arg)
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.appendEntries(ctx, res.Entries, entries); err != nil {
			return err
		}
		if !res.HasMore {
			return nil
		}
		arg = files.NewListFolderContinueArg(res.Cursor)
	}
}

// Walk traverses Dropbox metadata without caller cancellation.
func (d *driver) Walk(p string, fn func(storagecore.Entry) error) error {
	return d.WalkContext(context.Background(), p, fn)
}

// WalkContext traverses Dropbox metadata in deterministic display-path order.
func (d *driver) WalkContext(ctx context.Context, p string, fn func(storagecore.Entry) error) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("%w: walk callback is required", storagecore.ErrForbidden)
	}
	full, err := d.fullPath(p)
	if err != nil {
		return err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return err
	}
	if normalized != "" {
		entry, err := d.StatContext(ctx, p)
		if err != nil {
			return err
		}
		if !entry.IsDir {
			return fn(entry)
		}
	}
	arg := files.NewListFolderArg(full)
	arg.Recursive = true
	if err := d.walkPage(ctx, arg, fn); err != nil {
		if normalized == "" && errors.Is(err, storagecore.ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// Copy duplicates one Dropbox file without caller cancellation.
func (d *driver) Copy(src, dst string) error {
	return d.CopyContext(context.Background(), src, dst)
}

// CopyContext validates the source and treats identical normalized paths as a no-op.
func (d *driver) CopyContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalizedSrc, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	normalizedDst, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if normalizedSrc == "" || normalizedDst == "" {
		return fmt.Errorf("%w: copy requires non-root paths", storagecore.ErrForbidden)
	}
	data, err := d.GetContext(ctx, src)
	if err != nil {
		return err
	}
	if normalizedSrc == normalizedDst {
		return nil
	}
	return d.PutContext(ctx, dst, data)
}

// Move relocates one Dropbox path without caller cancellation.
func (d *driver) Move(src, dst string) error {
	return d.MoveContext(context.Background(), src, dst)
}

// MoveContext creates destination parents before moving a non-root Dropbox path.
func (d *driver) MoveContext(ctx context.Context, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalizedSrc, err := storagecore.NormalizePath(src)
	if err != nil {
		return err
	}
	normalizedDst, err := storagecore.NormalizePath(dst)
	if err != nil {
		return err
	}
	if normalizedSrc == "" || normalizedDst == "" {
		return fmt.Errorf("%w: move requires non-root paths", storagecore.ErrForbidden)
	}
	if _, err := d.StatContext(ctx, src); err != nil {
		return err
	}
	if normalizedSrc == normalizedDst {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(normalizedDst), strings.ToLower(normalizedSrc)+"/") {
		return fmt.Errorf("%w: destination cannot be inside source", storagecore.ErrForbidden)
	}
	srcPath, err := d.fullPath(normalizedSrc)
	if err != nil {
		return err
	}
	dstPath, err := d.fullPath(normalizedDst)
	if err != nil {
		return err
	}
	if err := d.ensureParentDirectories(ctx, dstPath); err != nil {
		return err
	}
	_, err = d.client.MoveV2(files.NewRelocationArg(srcPath, dstPath))
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return wrapError(err)
	}
	return ctx.Err()
}

// URL requests a temporary Dropbox link without caller cancellation.
func (d *driver) URL(p string) (string, error) {
	return d.URLContext(context.Background(), p)
}

// URLContext returns Dropbox's temporary link for a file.
func (d *driver) URLContext(ctx context.Context, p string) (string, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", fmt.Errorf("%w: a file path is required", storagecore.ErrForbidden)
	}
	full, err := d.fullPath(normalized)
	if err != nil {
		return "", err
	}
	link, err := d.client.GetTemporaryLink(&files.GetTemporaryLinkArg{
		Path: full,
	})
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if err != nil {
		return "", wrapError(err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return link.Link, nil
}

// fullPath converts a logical path to Dropbox's absolute API form while keeping its root empty.
func (d *driver) fullPath(p string) (string, error) {
	normalized, err := storagecore.NormalizePath(p)
	if err != nil {
		return "", err
	}
	joined := storagecore.JoinPrefix(d.prefix, normalized)
	if joined == "" {
		return "", nil
	}
	return "/" + joined, nil
}

// stripPrefix removes the configured prefix case-insensitively while retaining display casing.
func (d *driver) stripPrefix(p string) string {
	display := strings.TrimPrefix(p, "/")
	if d.prefix == "" {
		return display
	}
	if strings.EqualFold(display, d.prefix) {
		return ""
	}
	if len(display) > len(d.prefix) && display[len(d.prefix)] == '/' && strings.EqualFold(display[:len(d.prefix)], d.prefix) {
		return display[len(d.prefix)+1:]
	}
	return display
}

// metadataPath prefers Dropbox's display path and falls back to its lowercase path.
func (d *driver) metadataPath(metadata files.Metadata) string {
	display := metadata.PathDisplay
	if display == "" {
		display = metadata.PathLower
	}
	return d.stripPrefix(display)
}

// ensureParentDirectories creates each missing folder above a Dropbox file path.
func (d *driver) ensureParentDirectories(ctx context.Context, full string) error {
	parent := path.Dir(full)
	if parent == "." || parent == "/" {
		return nil
	}
	return d.ensureDirectory(ctx, parent)
}

// ensureDirectory creates a Dropbox directory one segment at a time.
func (d *driver) ensureDirectory(ctx context.Context, full string) error {
	current := ""
	for _, part := range strings.Split(strings.TrimPrefix(full, "/"), "/") {
		if part == "" {
			continue
		}
		current += "/" + part
		if err := d.ensureSingleDirectory(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

// ensureSingleDirectory accepts an existing folder and safely handles concurrent creation.
func (d *driver) ensureSingleDirectory(ctx context.Context, full string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata, err := d.client.GetMetadata(files.NewGetMetadataArg(full))
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err == nil {
		if _, ok := metadata.(*files.FolderMetadata); ok {
			return nil
		}
		return fmt.Errorf("%w: %s is not a directory", storagecore.ErrForbidden, full)
	}
	if !isNotFound(err) {
		return wrapError(err)
	}
	if _, err := d.client.CreateFolderV2(files.NewCreateFolderArg(full)); err == nil {
		return ctx.Err()
	} else {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		// A concurrent creator can win between the lookup and create calls.
		metadata, lookupErr := d.client.GetMetadata(files.NewGetMetadataArg(full))
		if lookupErr == nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, ok := metadata.(*files.FolderMetadata); ok {
				return nil
			}
		}
		return wrapError(err)
	}
}

// appendEntries converts supported Dropbox metadata while preserving display casing.
func (d *driver) appendEntries(ctx context.Context, metadata []files.IsMetadata, entries *[]storagecore.Entry) error {
	for _, item := range metadata {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch entry := item.(type) {
		case *files.FileMetadata:
			*entries = append(*entries, storagecore.Entry{
				Path: d.metadataPath(entry.Metadata),
				Size: int64(entry.Size),
			})
		case *files.FolderMetadata:
			*entries = append(*entries, storagecore.Entry{
				Path:  d.metadataPath(entry.Metadata),
				IsDir: true,
			})
		}
	}
	return nil
}

// walkPage gathers all pages before emitting a deterministic traversal.
func (d *driver) walkPage(ctx context.Context, arg *files.ListFolderArg, fn func(storagecore.Entry) error) error {
	var entries []storagecore.Entry
	if err := d.listPage(ctx, arg, &entries); err != nil {
		return wrapError(err)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return nil
}

// wrapError maps typed Dropbox and transport errors to storagecore sentinels.
func wrapError(err error) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrNotFound, err)
	}
	if isForbidden(err) {
		return fmt.Errorf("%w: %w", storagecore.ErrForbidden, err)
	}
	return err
}

// isForbidden recognizes permission, invalid-path, conflict, and capacity failures.
func isForbidden(err error) bool {
	if statusCode, ok := dropboxStatusCode(err); ok && (statusCode == 401 || statusCode == 403) {
		return true
	}
	if lookup := dropboxLookupError(err); lookup != nil {
		switch lookup.Tag {
		case files.LookupErrorMalformedPath, files.LookupErrorRestrictedContent, files.LookupErrorLocked:
			return true
		}
	}
	if write := dropboxWriteError(err); write != nil {
		switch write.Tag {
		case files.WriteErrorMalformedPath, files.WriteErrorConflict, files.WriteErrorNoWritePermission,
			files.WriteErrorInsufficientSpace, files.WriteErrorDisallowedName, files.WriteErrorTeamFolder,
			files.WriteErrorOperationSuppressed:
			return true
		}
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "insufficient_permissions") ||
		strings.Contains(message, "no_write_permission") ||
		strings.Contains(message, "not_authorized") ||
		strings.Contains(message, "parent_rev") ||
		strings.Contains(message, "revision conflict")
}

// contextReader makes SDK upload reads observe caller cancellation.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Read returns the context failure before asking the underlying reader for more data.
func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// copyContext stops stream processing promptly when the caller cancels.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

// joinCleanup preserves a primary failure while retaining response cleanup errors.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// isNotFound recognizes typed lookup failures, HTTP 404 responses, and legacy summaries.
func isNotFound(err error) bool {
	if statusCode, ok := dropboxStatusCode(err); ok && statusCode == 404 {
		return true
	}
	if lookup := dropboxLookupError(err); lookup != nil {
		return lookup.Tag == files.LookupErrorNotFound
	}
	return strings.Contains(strings.ToLower(err.Error()), "not_found")
}

// dropboxStatusCode extracts HTTP status information retained by the SDK.
func dropboxStatusCode(err error) (int, bool) {
	var sdkErr dropbox.SDKInternalError
	if errors.As(err, &sdkErr) {
		return sdkErr.StatusCode, true
	}
	return 0, false
}

// dropboxLookupError extracts a lookup union from every route used by the driver.
func dropboxLookupError(err error) *files.LookupError {
	var downloadErr files.DownloadAPIError
	if errors.As(err, &downloadErr) && downloadErr.EndpointError != nil {
		return downloadErr.EndpointError.Path
	}
	var metadataErr files.GetMetadataAPIError
	if errors.As(err, &metadataErr) && metadataErr.EndpointError != nil {
		return metadataErr.EndpointError.Path
	}
	var linkErr files.GetTemporaryLinkAPIError
	if errors.As(err, &linkErr) && linkErr.EndpointError != nil {
		return linkErr.EndpointError.Path
	}
	var listErr files.ListFolderAPIError
	if errors.As(err, &listErr) && listErr.EndpointError != nil {
		return listErr.EndpointError.Path
	}
	var continueErr files.ListFolderContinueAPIError
	if errors.As(err, &continueErr) && continueErr.EndpointError != nil {
		return continueErr.EndpointError.Path
	}
	var deleteErr files.DeleteV2APIError
	if errors.As(err, &deleteErr) && deleteErr.EndpointError != nil {
		return deleteErr.EndpointError.PathLookup
	}
	var moveErr files.MoveV2APIError
	if errors.As(err, &moveErr) && moveErr.EndpointError != nil {
		return moveErr.EndpointError.FromLookup
	}
	return nil
}

// dropboxWriteError extracts a write union from mutating routes used by the driver.
func dropboxWriteError(err error) *files.WriteError {
	var uploadErr files.UploadAPIError
	if errors.As(err, &uploadErr) && uploadErr.EndpointError != nil && uploadErr.EndpointError.Path != nil {
		return uploadErr.EndpointError.Path.Reason
	}
	var createErr files.CreateFolderV2APIError
	if errors.As(err, &createErr) && createErr.EndpointError != nil {
		return createErr.EndpointError.Path
	}
	var deleteErr files.DeleteV2APIError
	if errors.As(err, &deleteErr) && deleteErr.EndpointError != nil {
		return deleteErr.EndpointError.PathWrite
	}
	var moveErr files.MoveV2APIError
	if errors.As(err, &moveErr) && moveErr.EndpointError != nil {
		if moveErr.EndpointError.To != nil {
			return moveErr.EndpointError.To
		}
		return moveErr.EndpointError.FromWrite
	}
	return nil
}
