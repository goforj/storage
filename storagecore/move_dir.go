package storagecore

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

type DirMoveStorage interface {
	MakeDirContext(ctx context.Context, p string) error
	DeleteContext(ctx context.Context, p string) error
	StatContext(ctx context.Context, p string) (Entry, error)
	WalkContext(ctx context.Context, p string, fn func(Entry) error) error
	GetContext(ctx context.Context, p string) ([]byte, error)
	PutContext(ctx context.Context, p string, contents []byte) error
}

func MoveDirContext(ctx context.Context, disk DirMoveStorage, src, dst string) error {
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
	if srcPath == dstPath {
		return nil
	}
	if strings.HasPrefix(dstPath, srcPath+"/") {
		return fmt.Errorf("%w: destination cannot be inside source directory", ErrForbidden)
	}
	srcEntry, err := disk.StatContext(ctx, srcPath)
	if err != nil {
		return err
	}
	if !srcEntry.IsDir {
		return fmt.Errorf("%w: source path is not a directory", ErrUnsupported)
	}
	if _, err := disk.StatContext(ctx, dstPath); err == nil {
		return fmt.Errorf("%w: destination already exists", ErrForbidden)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := disk.MakeDirContext(ctx, dstPath); err != nil {
		return err
	}

	var entries []Entry
	if err := disk.WalkContext(ctx, srcPath, func(entry Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Path == "" || entry.Path == srcPath {
			return nil
		}
		if !strings.HasPrefix(entry.Path, srcPath+"/") {
			return nil
		}
		entries = append(entries, entry)
		return nil
	}); err != nil {
		return err
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

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := strings.TrimPrefix(entry.Path, srcPath)
		rel = strings.TrimPrefix(rel, "/")
		targetPath := dstPath
		if rel != "" {
			targetPath = path.Join(dstPath, rel)
		}
		if entry.IsDir {
			if err := disk.MakeDirContext(ctx, targetPath); err != nil {
				return err
			}
			continue
		}
		data, err := disk.GetContext(ctx, entry.Path)
		if err != nil {
			return err
		}
		if err := disk.PutContext(ctx, targetPath, data); err != nil {
			return err
		}
	}

	slices.SortFunc(entries, func(a, b Entry) int {
		aDepth := strings.Count(a.Path, "/")
		bDepth := strings.Count(b.Path, "/")
		if aDepth != bDepth {
			return bDepth - aDepth
		}
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Path, b.Path)
	})

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := disk.DeleteContext(ctx, entry.Path); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if err := disk.DeleteContext(ctx, srcPath); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}
