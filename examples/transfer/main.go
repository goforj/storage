// Command transfer verifies sustained uploads and downloads through Storage's public API.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/dropboxstorage"
	"github.com/goforj/storage/driver/ftpstorage"
	"github.com/goforj/storage/driver/gcsstorage"
	"github.com/goforj/storage/driver/localstorage"
	"github.com/goforj/storage/driver/memorystorage"
	"github.com/goforj/storage/driver/rclonestorage"
	"github.com/goforj/storage/driver/redisstorage"
	"github.com/goforj/storage/driver/s3storage"
	"github.com/goforj/storage/driver/sftpstorage"
	"github.com/goforj/storage/storagecore"
)

const (
	mib          int64 = 1 << 20
	bucket             = "storage-transfer"
	demoUser           = "storage"
	demoPassword       = "storage-transfer"
	loopback           = "127.0.0.1"
)

var fixtureDrivers = []string{"local", "memory", "redis", "ftp", "sftp", "s3", "gcs", "rclone"}

// main cancels active transfers on interruption and reports failure after cleanup.
func main() {
	driver := flag.String("driver", "local", "driver, comma-separated drivers, or all")
	total := flag.Int64("bytes", 100_000_000, "bytes to upload and independently read back per driver")
	objectMiB := flag.Int64("object-mib", 32, "maximum object size in MiB, from 1 to 128")
	root := flag.String("scratch", "", "parent of temporary local disks")
	stream := flag.Bool("s3-stream", false, "use the S3 object streaming API")
	timeout := flag.Duration("timeout", 4*time.Hour, "time limit for the selected drivers")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	if err := run(ctx, *driver, *total, *objectMiB, *root, *stream); err != nil {
		log.Fatal(err)
	}
}

// selectDrivers expands all to the local fixtures and validates live Dropbox selection.
func selectDrivers(selection string, hasDropbox bool) ([]string, error) {
	if selection == "all" {
		fmt.Println("SKIP dropbox: select it explicitly to use a live Dropbox account")
		return fixtureDrivers, nil
	}
	selected := strings.Split(selection, ",")
	for i, name := range selected {
		name = strings.TrimSpace(name)
		if !slices.Contains(fixtureDrivers, name) && name != "dropbox" {
			return nil, fmt.Errorf("unknown driver %q", name)
		}
		if name == "dropbox" && !hasDropbox {
			return nil, errors.New("dropbox requires STORAGE_TRANSFER_DROPBOX_TOKEN")
		}
		selected[i] = name
	}
	return selected, nil
}

// run gives each selected driver an independent scratch directory and key namespace.
func run(ctx context.Context, selection string, total, objectMiB int64, root string, stream bool) error {
	if total <= 0 || objectMiB < 1 || objectMiB > 128 {
		return errors.New("bytes must be positive and object-mib must be between 1 and 128")
	}
	token := os.Getenv("STORAGE_TRANSFER_DROPBOX_TOKEN")
	drivers, err := selectDrivers(selection, token != "")
	if err != nil {
		return err
	}

	var failures []error
	for _, name := range drivers {
		if err := ctx.Err(); err != nil {
			return errors.Join(errors.Join(failures...), err)
		}
		if err := runDriver(ctx, name, total, min(total, objectMiB*mib), root, token, stream && name == "s3"); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", name, err)
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(failures...)
}

// runDriver builds the public disk and cleans up its resources after the last object.
func runDriver(ctx context.Context, name string, total, objectSize int64, root, token string, stream bool) (err error) {
	if name == "local" || name == "rclone" {
		root, err = os.MkdirTemp(root, "storage-transfer-")
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	}

	cfg, err := configuration(ctx, name, root, token)
	if err != nil {
		return err
	}
	disk, err := storage.BuildContext(ctx, cfg)
	if err != nil {
		return err
	}
	if closer, ok := disk.(io.Closer); ok {
		defer func() { err = errors.Join(err, closer.Close()) }()
	}

	mode := "byte slices"
	if stream {
		mode = "streaming"
	}
	fmt.Printf("START %s: bytes=%d object_bytes=%d mode=%s\n", name, total, objectSize, mode)
	return measure(ctx, name, disk, total, objectSize, stream)
}

// configuration uses the localhost Compose fixtures, plus optional live Dropbox credentials.
func configuration(ctx context.Context, name, root, token string) (storage.DriverConfig, error) {
	switch name {
	case "local":
		return localstorage.Config{Root: root}, nil
	case "memory":
		return memorystorage.Config{}, nil
	case "redis":
		return redisstorage.Config{Addr: loopback + ":16379"}, nil
	case "ftp":
		return ftpstorage.Config{Host: loopback, Port: 12121, User: demoUser, Password: demoPassword}, nil
	case "sftp":
		// The intentionally unverified host key belongs to the disposable loopback fixture.
		return sftpstorage.Config{Host: loopback, Port: 12222, User: demoUser, Password: demoPassword,
			Prefix: "upload", InsecureIgnoreHostKey: true}, nil
	case "s3":
		cfg := s3storage.Config{
			Bucket: bucket, Region: "us-east-1", Endpoint: "http://" + loopback + ":19090",
			AccessKeyID: demoUser, SecretAccessKey: demoPassword, UsePathStyle: true,
		}
		return cfg, s3storage.PrepareBucketContext(ctx, cfg)
	case "gcs":
		return gcsstorage.Config{Bucket: bucket, Endpoint: "http://" + loopback + ":14443"}, nil
	case "rclone":
		conf, err := rclonestorage.RenderLocal(rclonestorage.LocalRemote{Name: "scratch"})
		return rclonestorage.Config{Remote: "scratch:" + filepath.ToSlash(root), RcloneConfigData: conf}, err
	case "dropbox":
		return dropboxstorage.Config{Token: token, Prefix: os.Getenv("STORAGE_TRANSFER_DROPBOX_PREFIX")}, nil
	default:
		return nil, fmt.Errorf("unknown driver %q", name)
	}
}

// measure transfers each object once and reports the resulting verified byte count.
func measure(ctx context.Context, name string, disk storage.Storage, total, objectSize int64, stream bool) error {
	var objects storagecore.ObjectStorage
	if stream {
		var err error
		objects, err = storage.Objects(disk)
		if err != nil {
			return err
		}
	}

	// Keep the payload incompressible and include the offset so reads of another object fail verification.
	data := make([]byte, objectSize)
	rand.Read(data)
	runID := strings.ToLower(rand.Text())
	started := time.Now()
	nextProgress := started.Add(30 * time.Second)
	var uploaded, downloaded time.Duration
	var count int64
	for offset := int64(0); offset < total; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload := data[:min(int64(len(data)), total-offset)]
		if len(payload) >= 8 {
			binary.LittleEndian.PutUint64(payload, uint64(offset))
		}
		key := fmt.Sprintf("storage-transfer-%s-%d", runID, count)
		put, get, err := transfer(ctx, disk, objects, key, payload)
		if err != nil {
			return fmt.Errorf("%s after %d verified bytes: %w", key, offset, err)
		}
		uploaded += put
		downloaded += get
		offset += int64(len(payload))
		if time.Now().After(nextProgress) {
			fmt.Printf("PROGRESS %s: verified_bytes=%d\n", name, offset)
			nextProgress = time.Now().Add(30 * time.Second)
		}
	}
	fmt.Printf("PASS %s: verified_objects=%d uploaded_bytes=%d downloaded_bytes=%d elapsed=%s upload_MiB_s=%.1f download_MiB_s=%.1f\n",
		name, count, total, total, time.Since(started).Round(time.Millisecond),
		float64(total)/float64(mib)/uploaded.Seconds(), float64(total)/float64(mib)/downloaded.Seconds())
	return nil
}

// transfer verifies a complete round trip and removes the unique key even when a write fails ambiguously.
func transfer(ctx context.Context, disk storage.Storage, objects storagecore.ObjectStorage, key string, payload []byte) (put, get time.Duration, err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		deleteErr := disk.WithContext(cleanup).Delete(key)
		if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
			err = errors.Join(err, fmt.Errorf("delete %s: %w", key, deleteErr))
		}
	}()

	expected := sha256.Sum256(payload)
	disk = disk.WithContext(ctx)
	start := time.Now()
	if objects == nil {
		err = disk.Put(key, payload)
	} else {
		_, err = objects.PutObjectContext(ctx, key, bytes.NewReader(payload), storagecore.PutObjectOptions{Size: int64(len(payload))})
	}
	put = time.Since(start)
	if err != nil {
		return put, 0, fmt.Errorf("upload: %w", err)
	}

	start = time.Now()
	size, actual, err := readChecksum(ctx, disk, objects, key)
	get = time.Since(start)
	if err != nil {
		return put, get, fmt.Errorf("download: %w", err)
	}
	if size != int64(len(payload)) || actual != expected {
		return put, get, fmt.Errorf("content mismatch: read %d bytes, expected %d and matching SHA-256", size, len(payload))
	}
	return put, get, nil
}

// readChecksum checks the downloaded bytes while keeping S3 streaming reads bounded.
func readChecksum(ctx context.Context, disk storage.Storage, objects storagecore.ObjectStorage, key string) (int64, [sha256.Size]byte, error) {
	if objects == nil {
		data, err := disk.Get(key)
		if err != nil {
			return 0, [sha256.Size]byte{}, err
		}
		return int64(len(data)), sha256.Sum256(data), nil
	}
	reader, err := objects.OpenObjectContext(ctx, key, storagecore.OpenObjectOptions{})
	if err != nil {
		return 0, [sha256.Size]byte{}, err
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, reader)
	var checksum [sha256.Size]byte
	hasher.Sum(checksum[:0])
	return size, checksum, errors.Join(err, reader.Close())
}
