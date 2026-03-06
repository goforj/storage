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
.PHONY: tidy test integration integration-driver examples-test coverage bench bench-render

#----------------------
# Go helpers
#----------------------
GO_TEST_FLAGS ?= -count=1
GOCACHE ?= /tmp/storage-gocache
GOMODCACHE ?= /tmp/storage-gomodcache

tidy: ##@go Run go mod tidy
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go mod tidy

test: ##@go Run unit tests
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go test $(GO_TEST_FLAGS) ./...

examples-test: ##@go Run tests in the examples module
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && cd examples && GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go test $(GO_TEST_FLAGS) ./...

coverage: ##@go Generate combined unit + integration coverage for Codecov
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && GOCACHE_DIR="$(GOCACHE)" GOMODCACHE_DIR="$(GOMODCACHE)" scripts/coverage-codecov.sh

integration: ##@go Run the centralized integration matrix in ./integration (may require Docker)
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && cd integration && GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go test -tags=integration $(GO_TEST_FLAGS) ./all

integration-driver: ##@go Run a single backend in the centralized integration matrix: make integration-driver gcs
	test -n "$(RUN_ARGS)" || (echo "usage: make integration-driver <driver>" && exit 1)
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && cd integration && INTEGRATION_DRIVER="$(firstword $(RUN_ARGS))" GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go test -tags=integration $(GO_TEST_FLAGS) ./all

bench: ##@go Run benchmark suites in ./docs/bench
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && cd docs/bench && GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go test -tags=bench -run '^$$' -bench . -count=1

bench-render: ##@go Render benchmark artifacts and update README benchmark embeds
	mkdir -p "$(GOCACHE)" "$(GOMODCACHE)" && cd docs/bench && GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go test -tags=benchrender -run TestRenderBenchmarks -count=1 -v
