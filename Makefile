# Parse makefile arguments (allows: make target arg1 arg2)
RUN_ARGS := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
$(eval $(RUN_ARGS):;@:)

# Silence GNU Make unless VERBOSE=1
ifndef VERBOSE
MAKEFLAGS += --no-print-directory
endif

# Terminal colors
GREEN  := $(shell tput -Txterm setaf 2)
WHITE  := $(shell tput -Txterm setaf 7)
YELLOW := $(shell tput -Txterm setaf 3)
RESET  := $(shell tput -Txterm sgr0)

# Help plumbing
.PHONY: help

HELP_FUN = \
%help; \
while(<>) { \
  if (/^([a-zA-Z0-9\-\_]+)\s*:.*\#\#(?:@([a-zA-Z0-9\-\_]+))?\s(.*)$$/) { \
    my $$target = $$1; \
    my $$cat = $$2 || "other"; \
    my $$desc = $$3; \
    push @{$$help{$$cat}}, [$$target, $$desc]; \
    $$GLOBAL_MAX = length($$target) if length($$target) > $$GLOBAL_MAX; \
  } \
} \
print "\n"; \
my $$max = $$GLOBAL_MAX; \
for my $$cat (sort keys %help) { \
  print "${WHITE}$$cat${RESET}\n"; \
  for my $$entry (@{$$help{$$cat}}) { \
    printf "  ${YELLOW}%-*s${RESET}  ${GREEN}%s${RESET}\n", $$max, $$entry->[0], $$entry->[1]; \
  } \
} \
print "";

help: ##@other Show this help.
	@perl -e '$(HELP_FUN)' $(MAKEFILE_LIST)

#----------------------
# Dev helpers
#----------------------
.PHONY: tidy test integration integration-driver examples-test coverage bench bench-render check-modules tag-modules release-modules release-plan

#----------------------
# Go helpers
#----------------------
GO_TEST_FLAGS ?= -count=1

GO_CACHE_ENV = $(if $(GOCACHE),GOCACHE="$(GOCACHE)") $(if $(GOMODCACHE),GOMODCACHE="$(GOMODCACHE)")

tidy: ##@go Run go mod tidy
	$(GO_CACHE_ENV) go mod tidy

test: ##@go Run unit tests
	$(GO_CACHE_ENV) go test $(GO_TEST_FLAGS) ./...

examples-test: ##@go Run tests in the examples module
	cd examples && $(GO_CACHE_ENV) go test $(GO_TEST_FLAGS) ./...

coverage: ##@go Generate combined unit + integration coverage for Codecov
	$(GO_CACHE_ENV) scripts/coverage-codecov.sh

check-modules: ##@go Verify published module manifests do not rely on local replace wiring
	scripts/check-published-modules.sh

integration: ##@go Run the centralized integration matrix in ./integration (may require Docker)
	cd integration && $(GO_CACHE_ENV) go test -tags=integration $(GO_TEST_FLAGS) ./all

integration-driver: ##@go Run a single backend in the centralized integration matrix: make integration-driver gcs
	test -n "$(RUN_ARGS)" || (echo "usage: make integration-driver <driver>" && exit 1)
	cd integration && INTEGRATION_DRIVER="$(firstword $(RUN_ARGS))" $(GO_CACHE_ENV) go test -tags=integration $(GO_TEST_FLAGS) ./all

bench: ##@go Run benchmark suites in ./docs/bench
	cd docs/bench && $(GO_CACHE_ENV) go test -tags=bench -run '^$$' -bench . -count=1

bench-render: ##@go Render benchmark artifacts and update README benchmark embeds
	cd docs/bench && $(GO_CACHE_ENV) go test -tags=benchrender -run TestRenderBenchmarks -count=1 -v

tag-modules: ##@release Tag all Go modules: make tag-modules v0.1.0 [-- --dry-run]
	test -n "$(RUN_ARGS)" || (echo "usage: make tag-modules <version> [-- --dry-run|--push|--exclude <dir>]" && exit 1)
	bash scripts/tag-all-modules.sh $(RUN_ARGS)

release-plan: ##@release Preview version rewrites and tags without changing files: make release-plan v0.1.0 [-- --exclude <dir>]
	test -n "$(RUN_ARGS)" || (echo "usage: make release-plan <version> [-- --exclude <dir>]" && exit 1)
	bash scripts/release-all-modules.sh $(RUN_ARGS) --dry-run --allow-dirty

release-modules: ##@release Rewrite sibling versions, commit, push the branch, and push tags: make release-modules v0.1.0 [-- --remote <name>|--exclude <dir>|--skip-existing]
	test -n "$(RUN_ARGS)" || (echo "usage: make release-modules <version> [-- --remote <name>|--exclude <dir>|--skip-existing]" && exit 1)
	bash scripts/release-all-modules.sh $(RUN_ARGS) --commit --push --allow-dirty
