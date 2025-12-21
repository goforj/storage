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
RCLONE_LINK ?= filesystem-rclone
RCLONE_SRC  ?= ../filesystem-rclone

.PHONY: dev-link-rclone dev-unlink-rclone tidy test

dev-link-rclone: ##@dev Symlink the filesystem-rclone repo into this repo (set RCLONE_SRC if not ../filesystem-rclone)
	@if [ -L "$(RCLONE_LINK)" ] || [ -e "$(RCLONE_LINK)" ]; then \
		echo "$(RCLONE_LINK) already exists; skipping symlink"; \
	else \
		if [ ! -d "$(RCLONE_SRC)" ]; then \
			echo "RCLONE_SRC '$(RCLONE_SRC)' not found; clone filesystem-rclone alongside this repo or override RCLONE_SRC"; \
			exit 1; \
		fi; \
		ln -s "$(RCLONE_SRC)" "$(RCLONE_LINK)"; \
		echo "Linked $(RCLONE_LINK) -> $(RCLONE_SRC)"; \
	fi
	@# uncomment replace lines in examples/rclone_local/go.mod if present
	@go run ./scripts/devreplace -file examples/rclone_local/go.mod -module github.com/goforj/filesystem-rclone -enable

dev-unlink-rclone: ##@dev Remove the filesystem-rclone symlink
	@if [ -L "$(RCLONE_LINK)" ]; then rm "$(RCLONE_LINK)"; echo "Removed symlink $(RCLONE_LINK)"; else echo "No symlink $(RCLONE_LINK) to remove"; fi
	@# re-comment replace lines in examples/rclone_local/go.mod if present
	@go run ./scripts/devreplace -file examples/rclone_local/go.mod -module github.com/goforj/filesystem-rclone -disable

#----------------------
# Go helpers
#----------------------
tidy: ##@go Run go mod tidy
	go mod tidy

test: ##@go Run unit tests
	go test ./...

integration: ##@go Run integration tests across core and add-on (requires RUN_INTEGRATION=1 and filesystem-rclone present)
	GOFLAGS=-tags=integration RUN_INTEGRATION=1 go test -count=1 ./...
	@if [ -d "$(RCLONE_LINK)" ]; then \
		(cd $(RCLONE_LINK) && GOFLAGS=-tags=integration RUN_INTEGRATION=1 go test -count=1 ./...); \
	fi
