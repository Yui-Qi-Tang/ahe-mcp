GO ?= go
BIN_DIR ?= bin
CORE_COMMANDS := \
	ahe-consistency-worker \
	ahe-migrate \
	ahe-runtime-admin \
	ahe-mcp-launch \
	ahe-query-mcp \
	ahe-ingest-mcp

ADAPTER_COMMANDS := \
	ahe-mcp-atlassian-adapter \
	ahe-mcp-codegraph-adapter

CORE_BINARIES := $(addprefix $(BIN_DIR)/,$(CORE_COMMANDS))
ADAPTER_BINARIES := $(addprefix $(BIN_DIR)/,$(ADAPTER_COMMANDS))
ALL_BINARIES := $(CORE_BINARIES) $(ADAPTER_BINARIES)

.PHONY: build adapters build-all test verify evidence-boundary evidence-and-case evidence-stale-review-case evidence-implements-case force

build: $(CORE_BINARIES)

adapters: $(ADAPTER_BINARIES)

# Synthetic domain-contract experiment; no PostgreSQL or model needed.
evidence-boundary:
	$(GO) test -mod=readonly -count=1 -run '^TestEvidenceBoundaryExperiment$$' -v ./internal/evidenceingestion

evidence-and-case:
	$(GO) test -mod=readonly -count=1 -run '^TestEvidenceBoundaryANDCase$$' -v ./internal/evidenceprojection

# Requires an explicitly selected, disposable non-production PostgreSQL database.
evidence-stale-review-case:
	@test -n "$$DATABASE_DSN" || { echo 'Set DATABASE_DSN to a disposable non-production database.' >&2; exit 1; }
	$(GO) test -mod=readonly -tags integration -count=1 -run '^TestIntegrationEvidenceBoundaryStaleReviewCase$$' -v ./internal/evidenceingestion

# Requires schema/role creation on an isolated disposable database, never production.
evidence-implements-case:
	@test -n "$$AHE_DBROLE_ACCEPTANCE_DATABASE_DSN" || { echo 'Set AHE_DBROLE_ACCEPTANCE_DATABASE_DSN to a disposable non-production database.' >&2; exit 1; }
	$(GO) test -mod=readonly -tags integration -count=1 -run '^TestIntegrationEvidenceBoundaryImplementsCase$$' -v ./internal/mcpintegration

build-all: $(ALL_BINARIES)

# Bound package concurrency without weakening per-test deadlines.
test:
	$(GO) test -mod=readonly -p 2 -count=1 ./...

verify:
	$(GO) test -mod=readonly -p 2 -count=1 ./...
	$(GO) build -mod=readonly ./...
	$(GO) vet -mod=readonly ./...

force:

$(BIN_DIR):
	mkdir -p "$@"

$(ALL_BINARIES): force | $(BIN_DIR)
	$(GO) build -mod=readonly -trimpath -o "$@" "./cmd/$(notdir $@)"
	chmod 0700 "$@"
