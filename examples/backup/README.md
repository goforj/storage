# Backup example

The same Go job saves, lists, and restores a snapshot on a local disk or on
RustFS via the S3 driver. The config selects the backend for the `backups`
disk.

Run from the repository root with Go 1.26 or newer. The RustFS run uses
Docker Compose and binds its S3 endpoint to localhost.

```sh
go run ./examples/backup
docker compose -f examples/backup/compose.yaml up -d --wait
go run ./examples/backup -backend rustfs
```

Both runs print the same result.

```text
Saved: snapshots/config.json
Found: snapshots/config.json
Restored: {"site":"example","version":1}
```

Stop RustFS with `docker compose -f examples/backup/compose.yaml down`.

[Example source](main.go) · [GoForj backup adapter](https://github.com/goforj/goforj/blob/main/internal/backup/remote_storage.go)
