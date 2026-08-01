# Parse makefile arguments (allows: make target arg1 arg2)
RUN_ARGS := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
$(eval $(RUN_ARGS):;@:)

# Silence GNU Make unless VERBOSE=1
ifndef VERBOSE
MAKEFLAGS += --no-print-directory
endif

# Help plumbing
.PHONY: help integration

HELP_FUN = %help; while (<>) { /^([A-Za-z0-9_-]+)\s*:.*\#\#(?:@([A-Za-z0-9_-]+))?\s(.*)$$/ or next; push @{$$help{$$2 || "other"}}, [$$1, $$3]; $$width = length($$1) if length($$1) > $$width } print "\e[1;97m$(or $(HELP_NAME),$(notdir $(CURDIR)))\e[0m\n\n"; for $$category (sort keys %help) { print "\e[1;97m$$category\e[0m\n"; for $$entry (@{$$help{$$category}}) { printf "  \e[1;32m%-*s\e[0m  \e[90m%s\e[0m\n", $$width, $$entry->[0], $$entry->[1] } }

help: ##@other Show this help.
	@perl -e '$(HELP_FUN)' $(MAKEFILE_LIST)

#----------------------
# Go helpers
#----------------------
GO_TEST_FLAGS ?= -count=1

tidy: ##@go Run go mod tidy
	go mod tidy

test: ##@go Run unit tests
	go test $(GO_TEST_FLAGS) ./...

examples-test: ##@go Run tests in the examples module
	cd examples && go test $(GO_TEST_FLAGS) ./...

coverage: ##@go Generate combined unit + integration coverage for Codecov
	scripts/coverage-codecov.sh

check-modules: ##@go Verify published module manifests do not rely on local replace wiring
	scripts/check-published-modules.sh

integration: ##@go Run the centralized integration matrix in ./integration (may require Docker)
	cd integration && go test -tags=integration $(GO_TEST_FLAGS) ./all

integration-driver: ##@go Run a single backend in the centralized integration matrix: make integration-driver gcs
	test -n "$(RUN_ARGS)" || (echo "usage: make integration-driver <driver>" && exit 1)
	cd integration && INTEGRATION_DRIVER="$(firstword $(RUN_ARGS))" go test -tags=integration $(GO_TEST_FLAGS) ./all

bench: ##@go Run benchmark suites in ./docs/bench
	cd docs/bench && go test -tags=bench -run '^$$' -bench . -count=1

bench-render: ##@go Render benchmark artifacts and update README benchmark embeds
	cd docs/bench && go test -tags=benchrender -run TestRenderBenchmarks -count=1 -v

tag-modules: ##@release Tag all Go modules: make tag-modules v0.1.0 [-- --dry-run]
	test -n "$(RUN_ARGS)" || (echo "usage: make tag-modules <version> [-- --dry-run|--push|--exclude <dir>]" && exit 1)
	bash scripts/tag-all-modules.sh $(RUN_ARGS)

release-plan: ##@release Preview version rewrites and tags without changing files: make release-plan v0.1.0 [-- --exclude <dir>]
	test -n "$(RUN_ARGS)" || (echo "usage: make release-plan <version> [-- --exclude <dir>]" && exit 1)
	bash scripts/release-all-modules.sh $(RUN_ARGS) --dry-run --allow-dirty

release-modules: ##@release Rewrite sibling versions, publish tags, sync checksums, and push the branch: make release-modules v0.1.0 [-- --remote <name>|--exclude <dir>|--skip-existing]
	test -n "$(RUN_ARGS)" || (echo "usage: make release-modules <version> [-- --remote <name>|--exclude <dir>|--skip-existing]" && exit 1)
	bash scripts/release-all-modules.sh $(RUN_ARGS) --commit --push --allow-dirty
