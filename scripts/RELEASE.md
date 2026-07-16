# Release Notes

This repo is a multi-module Go repo. Release mechanics are different for:

- published runtime modules
- repo-only support modules

## Module Rules

Published runtime modules:

- `github.com/goforj/storage`
- `github.com/goforj/storage/storagecore`
- `github.com/goforj/storage/storagetest`
- `github.com/goforj/storage/driver/*`

These must be valid for external consumers without relying on local `replace`.

Repo support modules:

- `examples`
- `integration`
- `docs/bench`

These are allowed to use local `replace` directives for sibling modules. That is intentional. They are repo-local tooling and verification surfaces, not the public dependency contract.

Release tooling keeps their sibling requirements coordinated for tidy verification, but `tag-all-modules.sh` never tags them.

## Why

Go only honors `replace` directives from the main module being built.

That means:

- `replace` inside a published driver module does not help downstream consumers
- `replace` inside `examples`, `integration`, or `docs/bench` is fine because those modules are run from this repo

## Normal Release Flow

Preview the release:

```sh
make release-plan v0.2.3
```

Run the release:

```sh
make release-modules v0.2.3
```

`make release-modules` does this:

1. Rewrites intra-repo module requirements to the target version.
2. Runs `scripts/check-published-modules.sh`.
3. Creates a release commit containing the touched `go.mod` files.
4. Tags every selected module from the release commit and pushes the tags.
5. Refreshes sibling `go.sum` entries from those immutable tags and creates a checksum follow-up commit when needed.
6. Pushes the current branch at the checksum-clean commit.

## Important Constraints

- Published driver modules must never depend on sibling `v0.0.0`.
- Published driver modules must never rely on committed sibling `replace`.
- Support modules may keep local `replace` directives.
- Release tags remain on the manifest commit; the checksum follow-up is intentionally untagged because dependency `go.sum` files are not part of the consumer contract.
- If checksum synchronization fails after tag publication, do not move or recreate tags. Fix the branch and rerun with `--skip-existing`.
- A `--skip-existing` retry synchronizes every selected module with sibling requirements, even when its manifest already names the target version.

Pushing a tag transfers its release commit even before the branch advances. Keeping tags immutable ensures proxy and consumer resolution remain stable while the branch records post-tag checksums.

## Validation

Published runtime module validation:

```sh
bash scripts/check-published-modules.sh
```

Release preview:

```sh
make release-plan v0.2.3
```

## Coverage and Repo-Local Testing

Repo-local coverage and tests should resolve through the workspace.

Do not force `GOWORK=off` for repo-local driver coverage runs unless you are explicitly testing isolated external-consumer behavior.

## If Release Resolution Breaks

Check these first:

1. Was the release commit pushed to the remote branch?
2. Were the tags pushed?
3. Did a published runtime module accidentally keep a sibling `replace` or `v0.0.0`?
4. Did a support module get treated like a published module?
