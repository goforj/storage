package storagecore

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// DirMoveStorage contains the operations needed to move a directory tree.
type DirMoveStorage interface {
	MakeDirContext(ctx context.Context, p string) error
	DeleteContext(ctx context.Context, p string) error
	StatContext(ctx context.Context, p string) (Entry, error)
	WalkContext(ctx context.Context, p string, fn func(Entry) error) error
	GetContext(ctx context.Context, p string) ([]byte, error)
	PutContext(ctx context.Context, p string, contents []byte) error
}

// MoveDirContext moves a directory tree using a copy phase followed by source
// deletion. It rolls back destination entries when copying fails. Once source
// deletion begins, an error may leave the complete destination and the
// undeleted source remainder because deleting copied data would risk data loss.
// Walk callbacks may include the source root itself more than once; those root
// entries are ignored because the root is created and deleted explicitly.
func MoveDirContext(ctx context.Context, disk DirMoveStorage, src, dst string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	srcPath, err := NormalizePath(src)
	if err != nil {
		return err
	}
	dstPath, err := NormalizePath(dst)
	if err != nil {
		return err
	}
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("%w: directory move requires non-root paths", ErrForbidden)
	}
	if strings.HasPrefix(dstPath, srcPath+"/") {
		return fmt.Errorf("%w: destination cannot be inside source directory", ErrForbidden)
	}

	srcEntry, err := disk.StatContext(ctx, srcPath)
	if err != nil {
		return fmt.Errorf("storage: stat move source %q: %w", srcPath, err)
	}
	if !srcEntry.IsDir {
		return fmt.Errorf("%w: source path %q is not a directory", ErrUnsupported, srcPath)
	}
	if srcPath == dstPath {
		return nil
	}
	if _, err := disk.StatContext(ctx, dstPath); err == nil {
		return fmt.Errorf("%w: destination %q already exists", ErrForbidden, dstPath)
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("storage: stat move destination %q: %w", dstPath, err)
	}

	entries, err := collectMoveEntries(ctx, disk, srcPath)
	if err != nil {
		return err
	}
	created := make([]string, 0, len(entries)+1)
	created = append(created, dstPath)
	if err := disk.MakeDirContext(ctx, dstPath); err != nil {
		return rollbackMove(ctx, disk, created, fmt.Errorf("storage: create move destination %q: %w", dstPath, err))
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return rollbackMove(ctx, disk, created, err)
		}
		rel := strings.TrimPrefix(entry.Path, srcPath+"/")
		targetPath := path.Join(dstPath, rel)
		if entry.IsDir {
			created = append(created, targetPath)
			if err := disk.MakeDirContext(ctx, targetPath); err != nil {
				return rollbackMove(ctx, disk, created, fmt.Errorf("storage: create move directory %q: %w", targetPath, err))
			}
			continue
		}
		data, err := disk.GetContext(ctx, entry.Path)
		if err != nil {
			return rollbackMove(ctx, disk, created, fmt.Errorf("storage: read move source %q: %w", entry.Path, err))
		}
		created = append(created, targetPath)
		if err := disk.PutContext(ctx, targetPath, data); err != nil {
			return rollbackMove(ctx, disk, created, fmt.Errorf("storage: write move destination %q: %w", targetPath, err))
		}
	}

	deletionStarted := false
	for i := len(entries) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			if !deletionStarted {
				return rollbackMove(ctx, disk, created, err)
			}
			return err
		}
		entry := entries[i]
		deletionStarted = true
		if err := disk.DeleteContext(ctx, entry.Path); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("storage: delete move source %q: %w", entry.Path, err)
		}
	}
	if err := ctx.Err(); err != nil {
		if !deletionStarted {
			return rollbackMove(ctx, disk, created, err)
		}
		return err
	}
	deletionStarted = true
	if err := disk.DeleteContext(ctx, srcPath); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("storage: delete move source %q: %w", srcPath, err)
	}
	return nil
}

// collectMoveEntries validates and sorts the full source tree before any
// destination mutation so walk failures cannot leave partial destinations.
func collectMoveEntries(ctx context.Context, disk DirMoveStorage, srcPath string) ([]Entry, error) {
	var entries []Entry
	seen := make(map[string]struct{})
	err := disk.WalkContext(ctx, srcPath, func(entry Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Path == "" || entry.Path == srcPath {
			return nil
		}
		entryPath, err := NormalizePath(entry.Path)
		if err != nil || entryPath != entry.Path || !strings.HasPrefix(entryPath, srcPath+"/") {
			return fmt.Errorf("%w: walk returned path %q outside source %q", ErrForbidden, entry.Path, srcPath)
		}
		if _, exists := seen[entryPath]; exists {
			return fmt.Errorf("%w: walk returned duplicate path %q", ErrForbidden, entryPath)
		}
		seen[entryPath] = struct{}{}
		entry.Path = entryPath
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: walk move source %q: %w", srcPath, err)
	}

	slices.SortFunc(entries, func(a, b Entry) int {
		aDepth := strings.Count(a.Path, "/")
		bDepth := strings.Count(b.Path, "/")
		if aDepth != bDepth {
			return aDepth - bDepth
		}
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

// rollbackMove removes destination paths in reverse creation order using a
// context detached from caller cancellation so cancellation cannot skip cleanup.
func rollbackMove(ctx context.Context, disk DirMoveStorage, created []string, moveErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var cleanupErrs []error
	for i := len(created) - 1; i >= 0; i-- {
		if err := cleanupCtx.Err(); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("storage: move rollback deadline: %w", err))
			break
		}
		if err := disk.DeleteContext(cleanupCtx, created[i]); err != nil && !errors.Is(err, ErrNotFound) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("storage: roll back move destination %q: %w", created[i], err))
		}
	}
	if len(cleanupErrs) == 0 {
		return moveErr
	}
	return errors.Join(moveErr, errors.Join(cleanupErrs...))
}
