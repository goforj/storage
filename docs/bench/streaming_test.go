//go:build bench

package bench

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/goforj/storage"
	"github.com/goforj/storage/storagecore"
)

// BenchmarkS3Objects compares the byte-slice and streaming APIs against the same RustFS bucket.
func BenchmarkS3Objects(b *testing.B) {
	if os.Getenv("BENCH_WITH_DOCKER") != "1" && !explicitlySelectedDriver("s3") {
		b.Skip("set BENCH_DRIVER=s3 or BENCH_WITH_DOCKER=1")
	}
	ctx := context.Background()
	fixture, err := newS3BenchmarkFixture(ctx)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(fixture.cleanup)
	store, cleanup, err := fixture.newStore(ctx)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(cleanup)
	objects, err := storage.Objects(store)
	if err != nil {
		b.Fatal(err)
	}

	payload := make([]byte, transferSize)
	if err := store.Put("source", payload); err != nil {
		b.Fatal(err)
	}

	for _, op := range []struct {
		name string
		run  func() (int64, error)
	}{
		{"get_8mib", func() (int64, error) {
			data, err := store.Get("source")
			return int64(len(data)), err
		}},
		{"open_8mib", func() (int64, error) {
			object, err := objects.OpenObjectContext(ctx, "source", storagecore.OpenObjectOptions{})
			if err != nil {
				return 0, err
			}
			n, err := io.Copy(io.Discard, object)
			return n, errors.Join(err, object.Close())
		}},
		{"put_8mib", func() (int64, error) {
			return transferSize, store.Put("destination", payload)
		}},
		{"upload_8mib", func() (int64, error) {
			info, err := objects.PutObjectContext(ctx, "destination", bytes.NewReader(payload), storagecore.PutObjectOptions{Size: transferSize})
			return info.Size, err
		}},
	} {
		b.Run(op.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(transferSize)
			for b.Loop() {
				n, err := op.run()
				if err != nil {
					b.Fatal(err)
				}
				if n != transferSize {
					b.Fatalf("transferred %d bytes, want %d", n, transferSize)
				}
			}
		})
	}
}
