package detective

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MCPReadCycleStatusRunning   = "running"
	MCPReadCycleStatusCompleted = "completed"
	MCPReadCycleStatusFailed    = "failed"

	mcpReadBindingValidationRequestID = "detective-mcp-read-binding-validation"
)

// MCPReadSourceConfig pins one provider-neutral, read-only MCP invocation.
// Command environment and arguments are trusted operator input and must not
// contain credentials; the database stores arguments and only a command hash.
type MCPReadSourceConfig struct {
	ConnectorID                 string
	Provider                    string
	LogicalCapability           string
	AdapterName                 string
	AdapterVersion              string
	ProviderToolName            string
	ProviderToolInputSchemaHash string
	Arguments                   json.RawMessage
	Command                     mcpstdio.CommandConfig
}

// MCPReadSourceBindingInput attaches one exact MCP invocation to a registered
// mcp-read-document workspace source.
type MCPReadSourceBindingInput struct {
	WorkspaceID     string
	SourceBindingID string
	Config          MCPReadSourceConfig
}

// MCPReadSourceBinding is the immutable, non-secret persisted invocation identity.
type MCPReadSourceBinding struct {
	SourceBindingID             string
	WorkspaceID                 string
	SourceID                    string
	ConnectorID                 string
	Provider                    string
	LogicalCapability           string
	AdapterName                 string
	AdapterVersion              string
	ProviderToolName            string
	ProviderToolInputSchemaHash string
	Arguments                   json.RawMessage
	CommandHash                 string
	BindingHash                 string
	RegisteredAt                time.Time
}

// MCPReadCollectionCycle is one durable polling attempt. A new timer wake gets
// a new cycle; a crash resumes the single running cycle and its request ID.
type MCPReadCollectionCycle struct {
	ID                  string
	WorkspaceID         string
	SourceBindingID     string
	Number              int64
	BindingHash         string
	CollectionRequestID string
	Status              string
	ConnectorDeliveryID string
	SourceSnapshotID    string
	ProviderObjectID    string
	ProviderRevision    string
	DocumentID          string
	CoverageComplete    *bool
	CoverageTruncated   *bool
	CompletionReason    string
	FailureKind         string
	StartedAt           time.Time
	FinishedAt          *time.Time
}

// MCPReadSourceTickInput selects one exact registered source and invocation.
type MCPReadSourceTickInput struct {
	WorkspaceID     string
	SourceBindingID string
	Config          MCPReadSourceConfig
}

// MCPReadSourceTickResult reports durable polling state. Collection and
// processing results are observations; the cycle row remains authoritative.
type MCPReadSourceTickResult struct {
	Binding    MCPReadSourceBinding
	Cycle      MCPReadCollectionCycle
	Collection *MCPReadCollectionResult
	Processing *MCPReadProcessingResult
	Resumed    bool
	Deferred   bool
}

type preparedMCPReadSource struct {
	workspaceID     string
	sourceBindingID string
	config          MCPReadSourceConfig
	commandHash     string
	bindingHash     string
}

// RegisterMCPReadSourceBinding creates or exactly reuses one immutable MCP
// invocation binding. It never starts the configured process.
func RegisterMCPReadSourceBinding(
	ctx context.Context,
	pool *pgxpool.Pool,
	input MCPReadSourceBindingInput,
) (MCPReadSourceBinding, bool, error) {
	if pool == nil {
		return MCPReadSourceBinding{}, false, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareMCPReadSource(input.WorkspaceID, input.SourceBindingID, input.Config)
	if err != nil {
		return MCPReadSourceBinding{}, false, err
	}
	workspace, err := ResolveWorkspace(ctx, pool, prepared.workspaceID)
	if err != nil {
		return MCPReadSourceBinding{}, false, err
	}
	source, found := workspaceSourceByID(workspace.Sources, prepared.sourceBindingID)
	if !found {
		return MCPReadSourceBinding{}, false, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			prepared.sourceBindingID,
			prepared.workspaceID,
		)
	}
	if source.CapabilityName != SourceCapabilityMCPReadDocument ||
		source.CapabilityVersion != SourceCapabilityMCPReadDocumentVersion {
		return MCPReadSourceBinding{}, false, newDomainError(
			ErrorUnsupportedCapability,
			"source binding %s is not %s/%s",
			source.ID,
			SourceCapabilityMCPReadDocument,
			SourceCapabilityMCPReadDocumentVersion,
		)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return MCPReadSourceBinding{}, false, fmt.Errorf("beginning mcp read source registration: %w", err)
	}
	defer tx.Rollback(context.Background())

	tag, err := tx.Exec(ctx, `
		INSERT INTO detective_mcp_read_source_bindings (
			source_binding_id, workspace_id, source_id,
			connector_id, provider, logical_capability,
			adapter_name, adapter_version, provider_tool_name,
			provider_tool_input_schema_hash, arguments,
			command_hash, binding_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13)
		ON CONFLICT (source_binding_id) DO NOTHING
	`, source.ID, workspace.ID, source.SourceID,
		prepared.config.ConnectorID, prepared.config.Provider, prepared.config.LogicalCapability,
		prepared.config.AdapterName, prepared.config.AdapterVersion, prepared.config.ProviderToolName,
		prepared.config.ProviderToolInputSchemaHash, prepared.config.Arguments,
		prepared.commandHash, prepared.bindingHash,
	)
	if err != nil {
		return MCPReadSourceBinding{}, false, fmt.Errorf("inserting mcp read source binding: %w", err)
	}
	created := tag.RowsAffected() == 1
	binding, found, err := readMCPReadSourceBinding(ctx, tx, source.ID, true)
	if err != nil {
		return MCPReadSourceBinding{}, false, err
	}
	if !found {
		return MCPReadSourceBinding{}, false, newDomainError(
			ErrorWorkspaceConflict,
			"mcp read source binding %s disappeared during registration",
			source.ID,
		)
	}
	if err := ensureMCPReadSourceBindingMatches(prepared, source.SourceID, binding); err != nil {
		return MCPReadSourceBinding{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MCPReadSourceBinding{}, false, fmt.Errorf("committing mcp read source registration: %w", err)
	}
	return binding, !created, nil
}

// RunMCPReadSourceTick runs or resumes one durable read-only polling cycle.
// It creates no proposal, admission decision, graph relation, answer, or model call.
func RunMCPReadSourceTick(
	ctx context.Context,
	pool *pgxpool.Pool,
	input MCPReadSourceTickInput,
) (MCPReadSourceTickResult, error) {
	if pool == nil {
		return MCPReadSourceTickResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareMCPReadSource(input.WorkspaceID, input.SourceBindingID, input.Config)
	if err != nil {
		return MCPReadSourceTickResult{}, err
	}
	workspace, err := ResolveWorkspace(ctx, pool, prepared.workspaceID)
	if err != nil {
		return MCPReadSourceTickResult{}, err
	}
	source, found := workspaceSourceByID(workspace.Sources, prepared.sourceBindingID)
	if !found {
		return MCPReadSourceTickResult{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			prepared.sourceBindingID,
			prepared.workspaceID,
		)
	}
	if source.CapabilityName != SourceCapabilityMCPReadDocument ||
		source.CapabilityVersion != SourceCapabilityMCPReadDocumentVersion {
		return MCPReadSourceTickResult{}, newDomainError(
			ErrorUnsupportedCapability,
			"source binding %s is not %s/%s",
			source.ID,
			SourceCapabilityMCPReadDocument,
			SourceCapabilityMCPReadDocumentVersion,
		)
	}

	binding, cycle, resumed, err := reserveMCPReadCollectionCycle(ctx, pool, prepared, source.SourceID)
	if err != nil {
		return MCPReadSourceTickResult{}, err
	}
	result := MCPReadSourceTickResult{Binding: binding, Cycle: cycle, Resumed: resumed}
	execution, acquired, err := tryMCPReadCycleExecution(ctx, pool, cycle.ID)
	if err != nil {
		return result, err
	}
	if !acquired {
		result.Deferred = true
		return result, nil
	}
	defer execution.release()

	collectionInput := MCPReadCollectionInput{
		RequestID:                   cycle.CollectionRequestID,
		ConnectorID:                 prepared.config.ConnectorID,
		Provider:                    prepared.config.Provider,
		LogicalCapability:           prepared.config.LogicalCapability,
		AdapterName:                 prepared.config.AdapterName,
		AdapterVersion:              prepared.config.AdapterVersion,
		ProviderToolName:            prepared.config.ProviderToolName,
		ProviderToolInputSchemaHash: prepared.config.ProviderToolInputSchemaHash,
		Arguments:                   prepared.config.Arguments,
		Command:                     prepared.config.Command,
	}
	collection, err := CollectMCPReadDelivery(ctx, pool, collectionInput)
	if err != nil {
		failed, recordErr := failMCPReadCollectionCycle(ctx, pool, cycle, nil, err)
		if recordErr != nil {
			return result, fmt.Errorf("%w; recording mcp read cycle failure: %v", err, recordErr)
		}
		result.Cycle = failed
		return result, err
	}
	result.Collection = &collection

	processing, err := ProcessMCPReadDelivery(ctx, pool, collection.Receipt.ConnectorDeliveryID)
	if err != nil {
		failed, recordErr := failMCPReadCollectionCycle(ctx, pool, cycle, &collection, err)
		if recordErr != nil {
			return result, fmt.Errorf("%w; recording mcp read cycle failure: %v", err, recordErr)
		}
		result.Cycle = failed
		return result, err
	}
	result.Processing = &processing
	completed, err := completeMCPReadCollectionCycle(ctx, pool, cycle, collection, processing)
	if err != nil {
		return result, err
	}
	result.Cycle = completed
	return result, nil
}

type mcpReadCycleExecution struct {
	conn    *pgx.Conn
	cycleID string
}

func tryMCPReadCycleExecution(
	ctx context.Context,
	pool *pgxpool.Pool,
	cycleID string,
) (*mcpReadCycleExecution, bool, error) {
	connConfig := pool.Config().ConnConfig.Copy()
	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring mcp read cycle execution connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(
		ctx,
		`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`,
		cycleID,
	).Scan(&acquired); err != nil {
		_ = conn.Close(context.Background())
		return nil, false, fmt.Errorf("acquiring mcp read cycle execution lock: %w", err)
	}
	if !acquired {
		_ = conn.Close(context.Background())
		return nil, false, nil
	}
	return &mcpReadCycleExecution{conn: conn, cycleID: cycleID}, true, nil
}

func (e *mcpReadCycleExecution) release() {
	if e == nil || e.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.conn.Close(ctx)
	e.conn = nil
}

func prepareMCPReadSource(
	workspaceID string,
	sourceBindingID string,
	config MCPReadSourceConfig,
) (preparedMCPReadSource, error) {
	workspaceID, err := normalizeWorkspaceID(workspaceID)
	if err != nil {
		return preparedMCPReadSource{}, err
	}
	sourceBindingID, err = normalizeDetectiveHashID(sourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return preparedMCPReadSource{}, err
	}
	collection, err := prepareMCPReadCollection(MCPReadCollectionInput{
		RequestID:                   mcpReadBindingValidationRequestID,
		ConnectorID:                 config.ConnectorID,
		Provider:                    config.Provider,
		LogicalCapability:           config.LogicalCapability,
		AdapterName:                 config.AdapterName,
		AdapterVersion:              config.AdapterVersion,
		ProviderToolName:            config.ProviderToolName,
		ProviderToolInputSchemaHash: config.ProviderToolInputSchemaHash,
		Arguments:                   config.Arguments,
		Command:                     config.Command,
	})
	if err != nil {
		return preparedMCPReadSource{}, err
	}
	command, err := mcpstdio.NormalizeCommandConfig(config.Command)
	if err != nil {
		return preparedMCPReadSource{}, newDomainError(ErrorInvalidInput, "invalid mcp command: %v", err)
	}
	if !mcpstdio.IsMacOSLoopbackOnlyCommand(command) {
		return preparedMCPReadSource{}, newDomainError(
			ErrorInvalidInput,
			"mcp read source command must use the exact macOS loopback-only wrapper",
		)
	}
	config = MCPReadSourceConfig{
		ConnectorID:                 collection.input.ConnectorID,
		Provider:                    collection.input.Provider,
		LogicalCapability:           collection.input.LogicalCapability,
		AdapterName:                 collection.input.AdapterName,
		AdapterVersion:              collection.input.AdapterVersion,
		ProviderToolName:            collection.input.ProviderToolName,
		ProviderToolInputSchemaHash: collection.input.ProviderToolInputSchemaHash,
		Arguments:                   append(json.RawMessage(nil), collection.input.Arguments...),
		Command:                     command,
	}
	commandHash, err := orchestrationPayloadHash(command)
	if err != nil {
		return preparedMCPReadSource{}, err
	}
	bindingHash, err := orchestrationPayloadHash(struct {
		WorkspaceID     string          `json:"workspace_id"`
		SourceBindingID string          `json:"source_binding_id"`
		ConnectorID     string          `json:"connector_id"`
		RequestHash     string          `json:"request_hash"`
		CommandHash     string          `json:"command_hash"`
		Arguments       json.RawMessage `json:"arguments"`
	}{
		WorkspaceID: workspaceID, SourceBindingID: sourceBindingID,
		ConnectorID: config.ConnectorID, RequestHash: collection.requestHash,
		CommandHash: commandHash, Arguments: config.Arguments,
	})
	if err != nil {
		return preparedMCPReadSource{}, err
	}
	return preparedMCPReadSource{
		workspaceID: workspaceID, sourceBindingID: sourceBindingID,
		config: config, commandHash: commandHash, bindingHash: bindingHash,
	}, nil
}

func ensureMCPReadSourceBindingMatches(
	prepared preparedMCPReadSource,
	sourceID string,
	binding MCPReadSourceBinding,
) error {
	if binding.SourceBindingID != prepared.sourceBindingID ||
		binding.WorkspaceID != prepared.workspaceID ||
		binding.SourceID != sourceID ||
		binding.ConnectorID != prepared.config.ConnectorID ||
		binding.Provider != prepared.config.Provider ||
		binding.LogicalCapability != prepared.config.LogicalCapability ||
		binding.AdapterName != prepared.config.AdapterName ||
		binding.AdapterVersion != prepared.config.AdapterVersion ||
		binding.ProviderToolName != prepared.config.ProviderToolName ||
		binding.ProviderToolInputSchemaHash != prepared.config.ProviderToolInputSchemaHash ||
		!bytes.Equal(binding.Arguments, prepared.config.Arguments) ||
		binding.CommandHash != prepared.commandHash ||
		binding.BindingHash != prepared.bindingHash {
		return newDomainError(
			ErrorWorkspaceConflict,
			"mcp read source binding %s is already registered with different invocation authority",
			prepared.sourceBindingID,
		)
	}
	return nil
}

func reserveMCPReadCollectionCycle(
	ctx context.Context,
	pool *pgxpool.Pool,
	prepared preparedMCPReadSource,
	sourceID string,
) (MCPReadSourceBinding, MCPReadCollectionCycle, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, fmt.Errorf("beginning mcp read cycle reservation: %w", err)
	}
	defer tx.Rollback(context.Background())

	binding, found, err := readMCPReadSourceBinding(ctx, tx, prepared.sourceBindingID, true)
	if err != nil {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, err
	}
	if !found {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, newDomainError(
			ErrorSourceBindingNotFound,
			"mcp read source binding %s is not registered",
			prepared.sourceBindingID,
		)
	}
	if err := ensureMCPReadSourceBindingMatches(prepared, sourceID, binding); err != nil {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, err
	}
	running, found, err := readRunningMCPReadCollectionCycle(ctx, tx, prepared.workspaceID, prepared.sourceBindingID)
	if err != nil {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, err
	}
	if found {
		if running.BindingHash != prepared.bindingHash {
			return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, newDomainError(
				ErrorWorkspaceConflict,
				"running mcp read cycle %s uses different binding authority",
				running.ID,
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, fmt.Errorf("committing mcp read cycle resume: %w", err)
		}
		return binding, running, true, nil
	}

	var cycleNumber int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(cycle_number), 0) + 1
		FROM detective_mcp_read_collection_cycles
		WHERE workspace_id = $1 AND source_binding_id = $2
	`, prepared.workspaceID, prepared.sourceBindingID).Scan(&cycleNumber); err != nil {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, fmt.Errorf("allocating mcp read cycle number: %w", err)
	}
	cycle := MCPReadCollectionCycle{
		ID:                  mcpReadCollectionCycleID(prepared.workspaceID, prepared.sourceBindingID, cycleNumber, prepared.bindingHash),
		WorkspaceID:         prepared.workspaceID,
		SourceBindingID:     prepared.sourceBindingID,
		Number:              cycleNumber,
		BindingHash:         prepared.bindingHash,
		CollectionRequestID: mcpReadCollectionRequestID(prepared.workspaceID, prepared.sourceBindingID, cycleNumber, prepared.bindingHash),
		Status:              MCPReadCycleStatusRunning,
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO detective_mcp_read_collection_cycles (
			cycle_id, workspace_id, source_binding_id, cycle_number,
			binding_hash, collection_request_id, status
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING started_at
	`, cycle.ID, cycle.WorkspaceID, cycle.SourceBindingID, cycle.Number,
		cycle.BindingHash, cycle.CollectionRequestID, cycle.Status,
	).Scan(&cycle.StartedAt); err != nil {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, fmt.Errorf("inserting mcp read cycle: %w", err)
	}
	cycle.StartedAt = cycle.StartedAt.UTC()
	if err := tx.Commit(ctx); err != nil {
		return MCPReadSourceBinding{}, MCPReadCollectionCycle{}, false, fmt.Errorf("committing mcp read cycle reservation: %w", err)
	}
	return binding, cycle, false, nil
}

func completeMCPReadCollectionCycle(
	ctx context.Context,
	pool *pgxpool.Pool,
	cycle MCPReadCollectionCycle,
	collection MCPReadCollectionResult,
	processing MCPReadProcessingResult,
) (MCPReadCollectionCycle, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE detective_mcp_read_collection_cycles
		SET
			status = 'completed',
			connector_delivery_id = $2,
			source_snapshot_id = $3,
			provider_object_id = $4,
			provider_revision = $5,
			document_id = $6,
			coverage_complete = $7,
			coverage_truncated = $8,
			completion_reason = $9,
			finished_at = now()
		WHERE cycle_id = $1 AND status = 'running'
	`, cycle.ID,
		collection.Receipt.ConnectorDeliveryID,
		processing.Source.SourceSnapshotID,
		processing.ObjectID,
		processing.Revision,
		processing.DocumentID,
		processing.Coverage.Complete,
		processing.Coverage.Truncated,
		processing.Coverage.CompletionReason,
	)
	if err != nil {
		return MCPReadCollectionCycle{}, fmt.Errorf("completing mcp read cycle: %w", err)
	}
	if tag.RowsAffected() != 1 {
		persisted, found, readErr := readMCPReadCollectionCycle(ctx, pool, cycle.ID)
		if readErr != nil {
			return MCPReadCollectionCycle{}, readErr
		}
		if !found || persisted.Status != MCPReadCycleStatusCompleted {
			return MCPReadCollectionCycle{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"mcp read cycle %s changed before completion",
				cycle.ID,
			)
		}
		return persisted, nil
	}
	completed, found, err := readMCPReadCollectionCycle(ctx, pool, cycle.ID)
	if err != nil {
		return MCPReadCollectionCycle{}, err
	}
	if !found {
		return MCPReadCollectionCycle{}, newDomainError(ErrorOrchestrationRunConflict, "completed mcp read cycle %s disappeared", cycle.ID)
	}
	return completed, nil
}

func failMCPReadCollectionCycle(
	ctx context.Context,
	pool *pgxpool.Pool,
	cycle MCPReadCollectionCycle,
	collection *MCPReadCollectionResult,
	cause error,
) (MCPReadCollectionCycle, error) {
	failureKind := "internal_error"
	if kind, ok := KindOf(cause); ok {
		failureKind = string(kind)
	}
	var connectorDeliveryID any
	var objectID any
	var revision any
	var documentID any
	var coverageComplete any
	var coverageTruncated any
	var completionReason any
	if collection != nil {
		connectorDeliveryID = collection.Receipt.ConnectorDeliveryID
		objectID = collection.ObjectID
		revision = collection.Revision
		documentID = collection.DocumentID
		coverageComplete = collection.Coverage.Complete
		coverageTruncated = collection.Coverage.Truncated
		completionReason = collection.Coverage.CompletionReason
	}
	tag, err := pool.Exec(ctx, `
		UPDATE detective_mcp_read_collection_cycles
		SET
			status = 'failed',
			connector_delivery_id = $2,
			provider_object_id = $3,
			provider_revision = $4,
			document_id = $5,
			coverage_complete = $6,
			coverage_truncated = $7,
			completion_reason = $8,
			failure_kind = $9,
			finished_at = now()
		WHERE cycle_id = $1 AND status = 'running'
	`, cycle.ID, connectorDeliveryID, objectID, revision, documentID,
		coverageComplete, coverageTruncated, completionReason, failureKind,
	)
	if err != nil {
		return cycle, fmt.Errorf("failing mcp read cycle: %w", err)
	}
	if tag.RowsAffected() != 1 {
		persisted, found, readErr := readMCPReadCollectionCycle(ctx, pool, cycle.ID)
		if readErr != nil {
			return cycle, readErr
		}
		if !found || persisted.Status != MCPReadCycleStatusFailed || persisted.FailureKind != failureKind {
			return cycle, newDomainError(
				ErrorOrchestrationRunConflict,
				"mcp read cycle %s changed before failure recording",
				cycle.ID,
			)
		}
		return persisted, nil
	}
	failed, found, err := readMCPReadCollectionCycle(ctx, pool, cycle.ID)
	if err != nil {
		return cycle, err
	}
	if !found {
		return cycle, newDomainError(ErrorOrchestrationRunConflict, "failed mcp read cycle %s disappeared", cycle.ID)
	}
	return failed, nil
}

func readMCPReadSourceBinding(
	ctx context.Context,
	db interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	sourceBindingID string,
	forUpdate bool,
) (MCPReadSourceBinding, bool, error) {
	query := `
		SELECT
			source_binding_id, workspace_id, source_id,
			connector_id, provider, logical_capability,
			adapter_name, adapter_version, provider_tool_name,
			provider_tool_input_schema_hash, arguments::text,
			command_hash, binding_hash, registered_at
		FROM detective_mcp_read_source_bindings
		WHERE source_binding_id = $1
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var binding MCPReadSourceBinding
	var arguments []byte
	err := db.QueryRow(ctx, query, sourceBindingID).Scan(
		&binding.SourceBindingID, &binding.WorkspaceID, &binding.SourceID,
		&binding.ConnectorID, &binding.Provider, &binding.LogicalCapability,
		&binding.AdapterName, &binding.AdapterVersion, &binding.ProviderToolName,
		&binding.ProviderToolInputSchemaHash, &arguments,
		&binding.CommandHash, &binding.BindingHash, &binding.RegisteredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPReadSourceBinding{}, false, nil
	}
	if err != nil {
		return MCPReadSourceBinding{}, false, fmt.Errorf("reading mcp read source binding: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &object); err != nil {
		return MCPReadSourceBinding{}, false, fmt.Errorf("decoding persisted mcp read arguments: %w", err)
	}
	binding.Arguments, err = json.Marshal(object)
	if err != nil {
		return MCPReadSourceBinding{}, false, fmt.Errorf("canonicalizing persisted mcp read arguments: %w", err)
	}
	binding.RegisteredAt = binding.RegisteredAt.UTC()
	return binding, true, nil
}

func readRunningMCPReadCollectionCycle(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID string,
	sourceBindingID string,
) (MCPReadCollectionCycle, bool, error) {
	return scanMCPReadCollectionCycle(tx.QueryRow(ctx, `
		SELECT
			cycle_id, workspace_id, source_binding_id, cycle_number,
			binding_hash, collection_request_id, status,
			connector_delivery_id, source_snapshot_id,
			provider_object_id, provider_revision, document_id,
			coverage_complete, coverage_truncated, completion_reason,
			failure_kind, started_at, finished_at
		FROM detective_mcp_read_collection_cycles
		WHERE workspace_id = $1
		  AND source_binding_id = $2
		  AND status = 'running'
		FOR UPDATE
	`, workspaceID, sourceBindingID))
}

func readMCPReadCollectionCycle(
	ctx context.Context,
	pool *pgxpool.Pool,
	cycleID string,
) (MCPReadCollectionCycle, bool, error) {
	return scanMCPReadCollectionCycle(pool.QueryRow(ctx, `
		SELECT
			cycle_id, workspace_id, source_binding_id, cycle_number,
			binding_hash, collection_request_id, status,
			connector_delivery_id, source_snapshot_id,
			provider_object_id, provider_revision, document_id,
			coverage_complete, coverage_truncated, completion_reason,
			failure_kind, started_at, finished_at
		FROM detective_mcp_read_collection_cycles
		WHERE cycle_id = $1
	`, cycleID))
}

func scanMCPReadCollectionCycle(row pgx.Row) (MCPReadCollectionCycle, bool, error) {
	var cycle MCPReadCollectionCycle
	var connectorDeliveryID, sourceSnapshotID *string
	var objectID, revision, documentID *string
	var completionReason, failureKind *string
	err := row.Scan(
		&cycle.ID, &cycle.WorkspaceID, &cycle.SourceBindingID, &cycle.Number,
		&cycle.BindingHash, &cycle.CollectionRequestID, &cycle.Status,
		&connectorDeliveryID, &sourceSnapshotID,
		&objectID, &revision, &documentID,
		&cycle.CoverageComplete, &cycle.CoverageTruncated, &completionReason,
		&failureKind, &cycle.StartedAt, &cycle.FinishedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPReadCollectionCycle{}, false, nil
	}
	if err != nil {
		return MCPReadCollectionCycle{}, false, fmt.Errorf("reading mcp read cycle: %w", err)
	}
	if connectorDeliveryID != nil {
		cycle.ConnectorDeliveryID = *connectorDeliveryID
	}
	if sourceSnapshotID != nil {
		cycle.SourceSnapshotID = *sourceSnapshotID
	}
	if objectID != nil {
		cycle.ProviderObjectID = *objectID
	}
	if revision != nil {
		cycle.ProviderRevision = *revision
	}
	if documentID != nil {
		cycle.DocumentID = *documentID
	}
	if completionReason != nil {
		cycle.CompletionReason = *completionReason
	}
	if failureKind != nil {
		cycle.FailureKind = *failureKind
	}
	cycle.StartedAt = cycle.StartedAt.UTC()
	if cycle.FinishedAt != nil {
		finishedAt := cycle.FinishedAt.UTC()
		cycle.FinishedAt = &finishedAt
	}
	return cycle, true, nil
}

func mcpReadCollectionCycleID(workspaceID, sourceBindingID string, number int64, bindingHash string) string {
	payload, _ := json.Marshal(struct {
		Version         string `json:"version"`
		WorkspaceID     string `json:"workspace_id"`
		SourceBindingID string `json:"source_binding_id"`
		Number          int64  `json:"cycle_number"`
		BindingHash     string `json:"binding_hash"`
	}{
		Version: "detective-mcp-read-cycle-v1", WorkspaceID: workspaceID,
		SourceBindingID: sourceBindingID, Number: number, BindingHash: bindingHash,
	})
	return "detective-mcp-read-cycle:" + hashHex(payload)
}

func mcpReadCollectionRequestID(workspaceID, sourceBindingID string, number int64, bindingHash string) string {
	payload, _ := json.Marshal(struct {
		Version         string `json:"version"`
		WorkspaceID     string `json:"workspace_id"`
		SourceBindingID string `json:"source_binding_id"`
		Number          int64  `json:"cycle_number"`
		BindingHash     string `json:"binding_hash"`
	}{
		Version: "detective-mcp-read-collection-request-v1", WorkspaceID: workspaceID,
		SourceBindingID: sourceBindingID, Number: number, BindingHash: bindingHash,
	})
	return "detective-mcp-read-collect:" + hashHex(payload)
}
