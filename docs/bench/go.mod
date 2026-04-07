module github.com/goforj/storage/docs/bench

go 1.24.4

require (
	github.com/docker/go-connections v0.5.0
	github.com/fsouza/fake-gcs-server v1.52.3
	github.com/goforj/storage v0.2.6
	github.com/goforj/storage/driver/ftpstorage v0.2.6
	github.com/goforj/storage/driver/gcsstorage v0.2.6
	github.com/goforj/storage/driver/localstorage v0.2.6
	github.com/goforj/storage/driver/memorystorage v0.2.6
	github.com/goforj/storage/driver/rclonestorage v0.2.6
	github.com/goforj/storage/driver/redisstorage v0.2.6
	github.com/goforj/storage/driver/s3storage v0.2.6
	github.com/goforj/storage/driver/sftpstorage v0.2.6
	github.com/goforj/storage/storagetest v0.2.6
	github.com/goftp/server v0.0.0-20200708154336-f64f7c2d8a42
	github.com/testcontainers/testcontainers-go v0.31.0
)

replace github.com/goforj/storage/storagetest => ../../storagetest

replace github.com/goforj/storage => ../..

replace github.com/goforj/storage/storagecore => ../../storagecore

replace github.com/goforj/storage/driver/ftpstorage => ../../driver/ftpstorage

replace github.com/goforj/storage/driver/gcsstorage => ../../driver/gcsstorage

replace github.com/goforj/storage/driver/localstorage => ../../driver/localstorage

replace github.com/goforj/storage/driver/memorystorage => ../../driver/memorystorage

replace github.com/goforj/storage/driver/rclonestorage => ../../driver/rclonestorage

replace github.com/goforj/storage/driver/redisstorage => ../../driver/redisstorage

replace github.com/goforj/storage/driver/s3storage => ../../driver/s3storage

replace github.com/goforj/storage/driver/sftpstorage => ../../driver/sftpstorage
