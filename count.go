package storage

import "context"

type fileCounterWalker interface {
	Walk(p string, fn func(Entry) error) error
}

type fileCounterContextWalker interface {
	WalkContext(ctx context.Context, p string, fn func(Entry) error) error
}

// CountFiles returns the recursive count of non-directory entries under path.
//
// CountFiles uses Walk under the hood, so it works across drivers that support
// recursive traversal.
// @group Core
//
// Example: count files on a disk
//
//	disk, _ := storage.Build(localstorage.Config{
//		Root: "/tmp/storage-count-files",
//	})
//	_ = disk.MakeDir("docs/archive")
//	_ = disk.Put("docs/readme.txt", []byte("hello"))
//	_ = disk.Put("docs/archive/guide.txt", []byte("guide"))
//
//	total, _ := storage.CountFiles(disk, "docs")
//	fmt.Println(total)
//	// Output: 2
func CountFiles(disk fileCounterWalker, p string) (int, error) {
	return CountFilesContext(context.Background(), disk, p)
}

// CountFilesContext returns the recursive count of non-directory entries under
// path using the caller-provided context.
// @group Context
func CountFilesContext(ctx context.Context, disk any, p string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int
	walk := func(entry Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir {
			count++
		}
		return nil
	}
	if scoped, ok := disk.(interface{ WithContext(context.Context) Storage }); ok {
		bound := scoped.WithContext(ctx)
		if walker, ok := bound.(fileCounterWalker); ok {
			if err := walker.Walk(p, walk); err != nil {
				return 0, err
			}
			return count, nil
		}
	}
	if cs, ok := disk.(fileCounterContextWalker); ok {
		if err := cs.WalkContext(ctx, p, walk); err != nil {
			return 0, err
		}
		return count, nil
	}
	basic, ok := disk.(fileCounterWalker)
	if !ok {
		return 0, ErrUnsupported
	}
	if err := basic.Walk(p, walk); err != nil {
		return 0, err
	}
	return count, nil
}
