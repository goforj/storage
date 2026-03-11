# Release Notes

This repo is a multi-module Go repo. Release mechanics are different for:

- published runtime modules
- repo-only support modules

## Module Rules

Published runtime modules:

- `github.com/goforj/storage`
- `github.com/goforj/storage/driver/*`
- `github.com/goforj/storage/storagetest`

These must be valid for external consumers without relying on local `replace`.

Repo support modules:

- `examples`
- `integration`
- `docs/bench`

These are allowed to use local `replace` directives for sibling modules. That is intentional. They are repo-local tooling and verification surfaces, not the public dependency contract.

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
4. Pushes the current branch to the remote.
5. Tags every module from the resulting commit and pushes the tags.

## Important Constraints

- Published driver modules must never depend on sibling `v0.0.0`.
- Published driver modules must never rely on committed sibling `replace`.
- Support modules may keep local `replace` directives.
- The release commit must be pushed before or along with the tags.

If tags are pushed but the release commit is not, the Go proxy can fail to resolve released module versions correctly.

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
