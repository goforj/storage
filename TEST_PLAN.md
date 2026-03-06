# Test Plan

## Goal

Keep confidence high while the repository shifts to the `storage` multi-module shape.

The quality bar is:
- fast unit tests in each module
- shared contract assertions in `storagetest`
- one centralized integration matrix in `integration`

## Testing Layers

### 1. Root module

Scope:
- `storage` package semantics
- manager and registry behavior
- path normalization and traversal rejection
- stable error vocabulary

Priority areas:
- `NormalizePath`
- `JoinPrefix`
- manager construction and disk lookup failures
- duplicate driver registration behavior
- package-level semantics staying aligned with docs

### 2. Driver module unit tests

Scope:
- driver-local prefix handling
- error wrapping and not-found mapping
- unsupported URL behavior where applicable
- context cancellation branches where cheap to test

Priority areas:
- `local`: traversal rejection, modtime helper, URL unsupported
- `s3`: key building, list shaping, presign behavior, not-found normalization
- `gcs`: emulator-mode URL behavior, not-found normalization
- `ftp` / `sftp`: prefix handling and not-found mapping
- `dropbox`: pagination, temporary link behavior, not-found mapping
- `rclone`: init conflicts, prefix handling, unsupported/public-link branches

### 3. Shared contract tests

Scope:
- backend-agnostic semantics in `storagetest`

Current contract covers:
- `Put` / `Get`
- `Exists`
- `Delete`
- one-level `List`
- URL support vs `ErrUnsupported`
- `ErrNotFound` and traversal rejection behavior
- context cancellation
- optional `ModTime`
- optional `Walk` capability checks

### 4. Centralized integration matrix

Scope:
- real backend behavior through the `integration` module
- same contract run against every supported backend fixture

Current matrix:
- `local`
- `gcs`
- `ftp`
- `s3`
- `sftp`
- representative `rclone_local`

This matrix is the default integration path for the repository.
The repository should not carry parallel per-driver integration suites that only duplicate the shared storage contract.

## Commands

Unit tests in root:

```bash
GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test ./...
```

Examples module:

```bash
cd examples
GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test ./...
```

Centralized integration matrix:

```bash
cd integration
GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test -tags=integration ./all -count=1
```

Single backend in centralized integration matrix:

```bash
cd integration
INTEGRATION_DRIVER=gcs GOCACHE=/tmp/storage-gocache GOMODCACHE=/tmp/storage-gomodcache go test -tags=integration ./all -count=1
```

Make targets:

```bash
make test
make examples-test
make integration
make integration-driver gcs
```

## Current Guidance

- Prefer adding coverage to `storagetest` when the semantics should hold across drivers.
- Prefer adding coverage to `integration/all` when validating backend behavior across fixtures.
- Prefer adding backend-specific assertions to the centralized matrix only when they represent intended product behavior rather than ad hoc driver quirks.
- Keep `/tmp` caches for Go commands; do not use the repository working tree for Go cache state.
