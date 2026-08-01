# Parse makefile arguments (allows: make target arg1 arg2)
ARGS := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
$(eval $(ARGS):;@:)

# Silence GNU Make unless VERBOSE=1
ifndef VERBOSE
MAKEFLAGS += --no-print-directory
endif

# Help plumbing
.PHONY: help test-integration

HELP_FUN = %help; while (<>) { /^([A-Za-z0-9_-]+)\s*:.*\#\#(?:@([A-Za-z0-9_-]+))?\s(.*)$$/ or next; push @{$$help{$$2 || "other"}}, [$$1, $$3]; $$width = length($$1) if length($$1) > $$width } print "\e[1;97m$(or $(HELP_NAME),$(notdir $(CURDIR)))\e[0m\n\n"; for $$category (sort keys %help) { print "\e[1;97m$$category\e[0m\n"; for $$entry (@{$$help{$$category}}) { printf "  \e[1;32m%-*s\e[0m  \e[90m%s\e[0m\n", $$width, $$entry->[0], $$entry->[1] } }

help: ##@other Show this help.
	@perl -e '$(HELP_FUN)' $(MAKEFILE_LIST)

GO_TEST_FLAGS ?= -count=1

##@maintenance
tidy: ##@maintenance Run go mod tidy.
	go mod tidy

##@tests
test: ##@tests Run unit tests.
	go test $(GO_TEST_FLAGS) ./...

test-examples: ##@tests Run tests in the examples module.
	cd examples && go test $(GO_TEST_FLAGS) ./...

test-coverage: ##@tests Generate combined unit and integration coverage for Codecov.
	scripts/coverage-codecov.sh

test-integration: ##@tests Run integration tests: make test-integration [all|ftp|gcs|local|memory|redis|rclone_local|s3|sftp].
	cd integration && INTEGRATION_DRIVER="$(or $(firstword $(ARGS)),all)" go test -tags=integration $(GO_TEST_FLAGS) ./all

##@analysis
modules-check: ##@analysis Verify published module manifests do not rely on local replace wiring.
	scripts/check-published-modules.sh

##@benchmarks
bench: ##@benchmarks Run benchmark suites in ./docs/bench.
	cd docs/bench && go test -tags=bench -run '^$$' -bench . -count=1

bench-render: ##@benchmarks Render benchmark artifacts and update README benchmark embeds.
	cd docs/bench && go test -tags=benchrender -run TestRenderBenchmarks -count=1 -v

##@release
release-tag: ##@release Tag all Go modules: make release-tag v0.1.0 [-- --dry-run].
	test -n "$(ARGS)" || (echo "usage: make release-tag <version> [-- --dry-run|--push|--exclude <dir>]" && exit 1)
	bash scripts/tag-all-modules.sh $(ARGS)

release-plan: ##@release Preview version rewrites and tags without changing files: make release-plan v0.1.0 [-- --exclude <dir>].
	test -n "$(ARGS)" || (echo "usage: make release-plan <version> [-- --exclude <dir>]" && exit 1)
	bash scripts/release-all-modules.sh $(ARGS) --dry-run --allow-dirty

release-publish: ##@release Rewrite sibling versions, publish tags, sync checksums, and push the branch: make release-publish v0.1.0 [-- --remote <name>|--exclude <dir>|--skip-existing].
	test -n "$(ARGS)" || (echo "usage: make release-publish <version> [-- --remote <name>|--exclude <dir>|--skip-existing]" && exit 1)
	bash scripts/release-all-modules.sh $(ARGS) --commit --push --allow-dirty
