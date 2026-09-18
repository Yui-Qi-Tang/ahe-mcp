GO ?= go
BIN_DIR ?= bin
DETECTIVE_DIR := apps/detective
WAILS_VERSION := v2.15.0
DARWIN_CGO_FLAGS := -mmacosx-version-min=13.0
DETECTIVE_COMMANDS := detective detective-source-demo detective-news-source
DETECTIVE_BINARIES := $(addprefix $(BIN_DIR)/,$(DETECTIVE_COMMANDS))

CORE_COMMANDS := \
	ahe-migrate \
	ahe-runtime-admin \
	ahe-mcp-launch \
	ahe-detective \
	ahe-query-mcp \
	ahe-ingest-mcp

ADAPTER_COMMANDS := \
	ahe-mcp-atlassian-adapter \
	ahe-mcp-codegraph-adapter

CORE_BINARIES := $(addprefix $(BIN_DIR)/,$(CORE_COMMANDS))
ADAPTER_BINARIES := $(addprefix $(BIN_DIR)/,$(ADAPTER_COMMANDS))
ALL_BINARIES := $(CORE_BINARIES) $(ADAPTER_BINARIES)

.PHONY: build adapters build-all detective frontend test verify evidence-boundary desktop desktop-test desktop-startup-test desktop-dev desktop-trial force

build: $(CORE_BINARIES)

adapters: $(ADAPTER_BINARIES)

# Synthetic domain-contract experiment; no PostgreSQL, model or Desktop needed.
evidence-boundary:
	$(GO) test -mod=readonly -count=1 -run '^TestEvidenceBoundaryExperiment$$' -v ./internal/evidenceingestion

build-all: $(ALL_BINARIES) detective

detective: $(DETECTIVE_BINARIES)

# Clean checkouts need the real frontend assets before Go loads its embed.
frontend:
	npm --prefix $(DETECTIVE_DIR)/frontend ci --ignore-scripts
	npm --prefix $(DETECTIVE_DIR)/frontend run build

# Bound simultaneous test packages; keep per-test deadlines and race checks intact.
test: frontend
	$(GO) test -mod=readonly -p 2 -count=1 ./...

verify: frontend
	npm --prefix $(DETECTIVE_DIR)/frontend test
	$(GO) test -mod=readonly -p 2 -count=1 ./...
	$(GO) build -mod=readonly ./...
	$(GO) vet -mod=readonly ./...

desktop:
	cd $(DETECTIVE_DIR)/cmd/detective-desktop && CGO_CFLAGS='$(DARWIN_CGO_FLAGS)' CGO_LDFLAGS='$(DARWIN_CGO_FLAGS)' $(GO) run github.com/wailsapp/wails/v2/cmd/wails@$(WAILS_VERSION) build -platform darwin/arm64 -skipbindings -nosyncgomod -m
	$(GO) build -mod=readonly -o '$(DETECTIVE_DIR)/build/bin/detective-source-demo' ./$(DETECTIVE_DIR)/cmd/detective-source-demo
	install -m 0700 $(DETECTIVE_DIR)/scripts/preview/Start.command '$(DETECTIVE_DIR)/build/bin/Start.command'

desktop-trial: desktop
	/bin/sh $(DETECTIVE_DIR)/scripts/desktop-trial.sh

desktop-startup-test: desktop
	DETECTIVE_DESKTOP_TEST_BINARY='$(CURDIR)/$(DETECTIVE_DIR)/build/bin/AHE Detective.app/Contents/MacOS/AHE Detective' $(GO) test -mod=readonly -count=1 -run '^TestNativeDesktopStartup$$' -v ./$(DETECTIVE_DIR)/cmd/detective-desktop

desktop-test: verify
	$(GO) test -mod=readonly -p 2 -race -count=1 ./...

desktop-dev:
	cd $(DETECTIVE_DIR)/cmd/detective-desktop && CGO_CFLAGS='$(DARWIN_CGO_FLAGS)' CGO_LDFLAGS='$(DARWIN_CGO_FLAGS)' $(GO) run github.com/wailsapp/wails/v2/cmd/wails@$(WAILS_VERSION) dev

force:

$(BIN_DIR):
	mkdir -p "$@"

$(ALL_BINARIES): force | $(BIN_DIR)
	$(GO) build -mod=readonly -trimpath -o "$@" "./cmd/$(notdir $@)"
	chmod 0700 "$@"

$(DETECTIVE_BINARIES): force | $(BIN_DIR)
	$(GO) build -mod=readonly -trimpath -o "$@" "./$(DETECTIVE_DIR)/cmd/$(notdir $@)"
	chmod 0700 "$@"
