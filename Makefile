GO ?= go
BIN_DIR ?= bin

CORE_COMMANDS := \
	ahe-migrate \
	ahe-detective \
	ahe-query-mcp \
	ahe-ingest-mcp

ADAPTER_COMMANDS := \
	ahe-mcp-atlassian-adapter \
	ahe-mcp-codegraph-adapter

CORE_BINARIES := $(addprefix $(BIN_DIR)/,$(CORE_COMMANDS))
ADAPTER_BINARIES := $(addprefix $(BIN_DIR)/,$(ADAPTER_COMMANDS))
ALL_BINARIES := $(CORE_BINARIES) $(ADAPTER_BINARIES)

.PHONY: build adapters build-all test verify force

build: $(CORE_BINARIES)

adapters: $(ADAPTER_BINARIES)

build-all: $(ALL_BINARIES)

test:
	$(GO) test -count=1 ./...

verify:
	$(GO) test -count=1 ./...
	$(GO) build ./...
	$(GO) vet ./...

force:

$(BIN_DIR):
	mkdir -p "$@"

$(ALL_BINARIES): force | $(BIN_DIR)
	$(GO) build -trimpath -o "$@" "./cmd/$(notdir $@)"
	chmod 0700 "$@"
