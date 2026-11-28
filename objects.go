package storage

import "github.com/goforj/storage/storagecore"

// Objects returns a driver's streaming object capability. Each streaming call
// takes its own context. Unsupported drivers return ErrUnsupported.
// @group Streaming
//
// Example: stream an upload into the MinIO bucket from the backup demo
//
//	// Run the MinIO backup demo first to create the bucket.
//	ctx := context.Background()
//	disk, err := storage.Build(s3storage.Config{
//		Bucket:          "storage-demo",
//		Region:          "us-east-1",
//		Endpoint:        "http://127.0.0.1:19000",
//		UsePathStyle:    true,
//		AccessKeyID:     "storage-demo",
//		SecretAccessKey: "storage-demo-password",
//	})
//	if err != nil {
//		panic(err)
//	}
//	objects, err := storage.Objects(disk)
//	if err != nil {
//		panic(err)
//	}
//	body := strings.NewReader("hello from storage")
//	info, err := objects.PutObjectContext(ctx, "stream.txt", body, storagecore.PutObjectOptions{
//		Size: body.Size(),
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(info.Key, info.Size)
//	// Output: stream.txt 18
func Objects(disk Storage) (storagecore.ObjectStorage, error) {
	return storagecore.Objects(disk)
}
