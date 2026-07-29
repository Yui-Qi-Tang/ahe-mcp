package evidenceingestion

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// MCPReadSourceStateQuerySchemaV1 is the bounded source-state response contract.
	MCPReadSourceStateQuerySchemaV1 = "mcp-read-source-state-query-v1"

	// MCPReadSourceStateModeLatestObserved selects the latest successful local observation.
	MCPReadSourceStateModeLatestObserved = "latest_observed"
	// MCPReadSourceStateModeExactRevision selects one exact provider-owned revision.
	MCPReadSourceStateModeExactRevision = "exact_revision"
	// MCPReadSourceStateModeHistory lists distinct immutable states by local observation order.
	MCPReadSourceStateModeHistory = "history"
	// MCPReadSourceStateModeCompare selects two exact provider-owned revisions without merging them.
	MCPReadSourceStateModeCompare = "compare"

	MCPReadSourceStateDefaultLimit = 20
	MCPReadSourceStateMaxLimit     = 100
	mcpReadSourceRevisionMaxRunes  = 500
)

var mcpReadSourceStateLimitations = []string{
	"latest_observed means the highest completed AHE polling cycle, not a global remote-system current-state claim",
	"cycle_number orders local observations and is not provider revision ordering",
	"compare preserves two immutable source states and does not infer deletion, rename, equivalence, or causal change",
	"source-state inspection does not join evidence records across revisions",
	"global absence inference is not allowed",
}

// MCPReadSourceStateQueryInput selects one bounded read-only source-state view.
type MCPReadSourceStateQueryInput struct {
	SourceBindingID string `json:"source_binding_id"`
	Mode            string `json:"mode"`
	Revision        string `json:"revision,omitempty"`
	FromRevision    string `json:"from_revision,omitempty"`
	ToRevision      string `json:"to_revision,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

// MCPReadSourceStateBinding identifies the immutable MCP invocation binding.
type MCPReadSourceStateBinding struct {
	WorkspaceID       string `json:"workspace_id"`
	SourceBindingID   string `json:"source_binding_id"`
	SourceID          string `json:"source_id"`
	Provider          string `json:"provider"`
	LogicalCapability string `json:"logical_capability"`
}

// MCPReadSourceStateEvidenceFilter is an exact follow-up scope for evidence tools.
type MCPReadSourceStateEvidenceFilter struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	SourceID         string `json:"source_id"`
	SourceVersion    string `json:"source_version"`
}

// MCPReadSourceState is one immutable source snapshot plus bounded observation audit.
type MCPReadSourceState struct {
	SourceSnapshotID     string                           `json:"source_snapshot_id"`
	SourceSystem         string                           `json:"source_system"`
	SourceID             string                           `json:"source_id"`
	SourceVersion        string                           `json:"source_version"`
	RawContentHash       string                           `json:"raw_content_hash"`
	ProviderObjectID     string                           `json:"provider_object_id"`
	ProviderRevision     string                           `json:"provider_revision"`
	DocumentID           string                           `json:"document_id"`
	CoverageComplete     bool                             `json:"coverage_complete"`
	CoverageTruncated    bool                             `json:"coverage_truncated"`
	CompletionReason     string                           `json:"completion_reason"`
	FirstObservedCycleID string                           `json:"first_observed_cycle_id"`
	FirstObservedCycle   int64                            `json:"first_observed_cycle"`
	LastObservedCycleID  string                           `json:"last_observed_cycle_id"`
	LastObservedCycle    int64                            `json:"last_observed_cycle"`
	ObservationCount     int                              `json:"observation_count"`
	FirstObservedAt      time.Time                        `json:"first_observed_at"`
	LastObservedAt       time.Time                        `json:"last_observed_at"`
	LatestObserved       bool                             `json:"latest_observed"`
	ExactEvidenceFilter  MCPReadSourceStateEvidenceFilter `json:"exact_evidence_filter"`
}

// MCPReadSourceStateRef identifies one state in an explicit comparison.
type MCPReadSourceStateRef struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	ProviderRevision string `json:"provider_revision"`
	RawContentHash   string `json:"raw_content_hash"`
}

// MCPReadSourceStateComparison reports identity differences without semantic inference.
type MCPReadSourceStateComparison struct {
	From                       MCPReadSourceStateRef `json:"from"`
	To                         MCPReadSourceStateRef `json:"to"`
	SourceSnapshotChanged      bool                  `json:"source_snapshot_changed"`
	RawContentChanged          bool                  `json:"raw_content_changed"`
	CrossRevisionJoinPerformed bool                  `json:"cross_revision_join_performed"`
}

// MCPReadSourceStateQueryResult is a bounded, evidence-free source-state projection.
type MCPReadSourceStateQueryResult struct {
	SchemaVersion                 string                        `json:"schema_version"`
	Mode                          string                        `json:"mode"`
	Binding                       MCPReadSourceStateBinding     `json:"binding"`
	States                        []MCPReadSourceState          `json:"states"`
	Comparison                    *MCPReadSourceStateComparison `json:"comparison,omitempty"`
	Limit                         int                           `json:"limit"`
	Truncated                     bool                          `json:"truncated"`
	SourceVersionAuthority        string                        `json:"source_version_authority"`
	ObservationOrderingAuthority  string                        `json:"observation_ordering_authority"`
	GlobalAbsenceInferenceAllowed bool                          `json:"global_absence_inference_allowed"`
	Limitations                   []string                      `json:"limitations"`
}

type preparedMCPReadSourceStateQuery struct {
	sourceBindingID string
	mode            string
	revision        string
	fromRevision    string
	toRevision      string
	limit           int
}

// QueryMCPReadSourceStates reads one bounded source history in a read-only transaction.
func QueryMCPReadSourceStates(
	ctx context.Context,
	pool *pgxpool.Pool,
	input MCPReadSourceStateQueryInput,
) (MCPReadSourceStateQueryResult, error) {
	if pool == nil {
		return MCPReadSourceStateQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareMCPReadSourceStateQuery(input)
	if err != nil {
		return MCPReadSourceStateQueryResult{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return MCPReadSourceStateQueryResult{}, fmt.Errorf("beginning MCP source-state query: %w", err)
	}
	defer tx.Rollback(context.Background())

	binding, err := readMCPReadSourceStateBinding(ctx, tx, prepared.sourceBindingID)
	if err != nil {
		return MCPReadSourceStateQueryResult{}, err
	}
	latest, err := readMCPReadSourceStates(ctx, tx, prepared.sourceBindingID, "", 1)
	if err != nil {
		return MCPReadSourceStateQueryResult{}, err
	}
	if len(latest) == 0 {
		return MCPReadSourceStateQueryResult{}, newDomainError(
			ErrorSourceStateNotFound,
			"source binding %s has no completed source state",
			prepared.sourceBindingID,
		)
	}
	latestCycle := latest[0].LastObservedCycle
	result := MCPReadSourceStateQueryResult{
		SchemaVersion:                 MCPReadSourceStateQuerySchemaV1,
		Mode:                          prepared.mode,
		Binding:                       binding,
		States:                        []MCPReadSourceState{},
		Limit:                         prepared.limit,
		SourceVersionAuthority:        "provider_revision",
		ObservationOrderingAuthority:  "detective_mcp_read_collection_cycles.cycle_number",
		GlobalAbsenceInferenceAllowed: false,
		Limitations:                   append([]string(nil), mcpReadSourceStateLimitations...),
	}

	switch prepared.mode {
	case MCPReadSourceStateModeLatestObserved:
		result.States = latest
	case MCPReadSourceStateModeExactRevision:
		result.States, err = readExactMCPReadSourceState(
			ctx,
			tx,
			prepared.sourceBindingID,
			prepared.revision,
		)
	case MCPReadSourceStateModeHistory:
		result.States, err = readMCPReadSourceStates(
			ctx,
			tx,
			prepared.sourceBindingID,
			"",
			prepared.limit+1,
		)
		if len(result.States) > prepared.limit {
			result.States = result.States[:prepared.limit]
			result.Truncated = true
		}
	case MCPReadSourceStateModeCompare:
		var from, to []MCPReadSourceState
		from, err = readExactMCPReadSourceState(
			ctx,
			tx,
			prepared.sourceBindingID,
			prepared.fromRevision,
		)
		if err == nil {
			to, err = readExactMCPReadSourceState(
				ctx,
				tx,
				prepared.sourceBindingID,
				prepared.toRevision,
			)
		}
		if err == nil {
			result.States = []MCPReadSourceState{from[0], to[0]}
			result.Comparison = &MCPReadSourceStateComparison{
				From:                       mcpReadSourceStateRef(from[0]),
				To:                         mcpReadSourceStateRef(to[0]),
				SourceSnapshotChanged:      from[0].SourceSnapshotID != to[0].SourceSnapshotID,
				RawContentChanged:          from[0].RawContentHash != to[0].RawContentHash,
				CrossRevisionJoinPerformed: false,
			}
		}
	}
	if err != nil {
		return MCPReadSourceStateQueryResult{}, err
	}
	for index := range result.States {
		result.States[index].LatestObserved = result.States[index].LastObservedCycle == latestCycle
	}
	if err := tx.Commit(ctx); err != nil {
		return MCPReadSourceStateQueryResult{}, fmt.Errorf("committing MCP source-state query: %w", err)
	}
	return result, nil
}

func prepareMCPReadSourceStateQuery(
	input MCPReadSourceStateQueryInput,
) (preparedMCPReadSourceStateQuery, error) {
	sourceBindingID := strings.TrimSpace(input.SourceBindingID)
	if !strings.HasPrefix(sourceBindingID, "workspace-source:") ||
		len(sourceBindingID) != len("workspace-source:")+64 {
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidRecordID,
			"source_binding_id must be a workspace-source: SHA-256 identifier",
		)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(sourceBindingID, "workspace-source:")); err != nil {
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidRecordID,
			"source_binding_id must be a workspace-source: SHA-256 identifier",
		)
	}
	mode := strings.TrimSpace(input.Mode)
	switch mode {
	case MCPReadSourceStateModeLatestObserved,
		MCPReadSourceStateModeExactRevision,
		MCPReadSourceStateModeHistory,
		MCPReadSourceStateModeCompare:
	default:
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidInput,
			"mode must be latest_observed, exact_revision, history, or compare",
		)
	}
	revision, err := normalizeMCPReadSourceRevision(input.Revision, "revision")
	if err != nil {
		return preparedMCPReadSourceStateQuery{}, err
	}
	fromRevision, err := normalizeMCPReadSourceRevision(input.FromRevision, "from_revision")
	if err != nil {
		return preparedMCPReadSourceStateQuery{}, err
	}
	toRevision, err := normalizeMCPReadSourceRevision(input.ToRevision, "to_revision")
	if err != nil {
		return preparedMCPReadSourceStateQuery{}, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = MCPReadSourceStateDefaultLimit
	}
	if limit < 1 || limit > MCPReadSourceStateMaxLimit {
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidInput,
			"limit must be between 1 and %d",
			MCPReadSourceStateMaxLimit,
		)
	}
	if mode != MCPReadSourceStateModeHistory && input.Limit != 0 {
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidInput,
			"limit is allowed only in history mode",
		)
	}
	if mode == MCPReadSourceStateModeExactRevision && revision == "" {
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidInput,
			"revision is required in exact_revision mode",
		)
	}
	if mode != MCPReadSourceStateModeExactRevision && revision != "" {
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidInput,
			"revision is allowed only in exact_revision mode",
		)
	}
	if mode == MCPReadSourceStateModeCompare {
		if fromRevision == "" || toRevision == "" {
			return preparedMCPReadSourceStateQuery{}, newDomainError(
				ErrorInvalidInput,
				"from_revision and to_revision are required in compare mode",
			)
		}
		if fromRevision == toRevision {
			return preparedMCPReadSourceStateQuery{}, newDomainError(
				ErrorInvalidInput,
				"from_revision and to_revision must differ",
			)
		}
	} else if fromRevision != "" || toRevision != "" {
		return preparedMCPReadSourceStateQuery{}, newDomainError(
			ErrorInvalidInput,
			"from_revision and to_revision are allowed only in compare mode",
		)
	}
	switch mode {
	case MCPReadSourceStateModeLatestObserved, MCPReadSourceStateModeExactRevision:
		limit = 1
	case MCPReadSourceStateModeCompare:
		limit = 2
	}
	return preparedMCPReadSourceStateQuery{
		sourceBindingID: sourceBindingID,
		mode:            mode,
		revision:        revision,
		fromRevision:    fromRevision,
		toRevision:      toRevision,
		limit:           limit,
	}, nil
}

func normalizeMCPReadSourceRevision(value string, field string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > mcpReadSourceRevisionMaxRunes {
		return "", newDomainError(
			ErrorInvalidInput,
			"%s must be valid UTF-8 with at most %d characters",
			field,
			mcpReadSourceRevisionMaxRunes,
		)
	}
	return value, nil
}

func readMCPReadSourceStateBinding(
	ctx context.Context,
	tx pgx.Tx,
	sourceBindingID string,
) (MCPReadSourceStateBinding, error) {
	var binding MCPReadSourceStateBinding
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, source_binding_id, source_id, provider, logical_capability
		FROM detective_mcp_read_source_bindings
		WHERE source_binding_id = $1
	`, sourceBindingID).Scan(
		&binding.WorkspaceID,
		&binding.SourceBindingID,
		&binding.SourceID,
		&binding.Provider,
		&binding.LogicalCapability,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPReadSourceStateBinding{}, newDomainError(
			ErrorSourceStateNotFound,
			"MCP source binding %s was not found",
			sourceBindingID,
		)
	}
	if err != nil {
		return MCPReadSourceStateBinding{}, fmt.Errorf("reading MCP source-state binding: %w", err)
	}
	return binding, nil
}

func readExactMCPReadSourceState(
	ctx context.Context,
	tx pgx.Tx,
	sourceBindingID string,
	revision string,
) ([]MCPReadSourceState, error) {
	states, err := readMCPReadSourceStates(ctx, tx, sourceBindingID, revision, 2)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, newDomainError(
			ErrorSourceStateNotFound,
			"MCP source binding %s has no completed revision %q",
			sourceBindingID,
			revision,
		)
	}
	if len(states) != 1 {
		return nil, newDomainError(
			ErrorOccurrenceConflict,
			"MCP source binding %s revision %q maps to multiple immutable states",
			sourceBindingID,
			revision,
		)
	}
	return states, nil
}

func readMCPReadSourceStates(
	ctx context.Context,
	tx pgx.Tx,
	sourceBindingID string,
	revision string,
	limit int,
) ([]MCPReadSourceState, error) {
	rows, err := tx.Query(ctx, `
		SELECT
		  cycle.source_snapshot_id,
		  snapshot.source_system,
		  snapshot.source_id,
		  snapshot.source_version,
		  snapshot.raw_content_hash,
		  cycle.provider_object_id,
		  cycle.provider_revision,
		  cycle.document_id,
		  cycle.coverage_complete,
		  cycle.coverage_truncated,
		  cycle.completion_reason,
		  (array_agg(cycle.cycle_id ORDER BY cycle.cycle_number ASC))[1],
		  MIN(cycle.cycle_number),
		  (array_agg(cycle.cycle_id ORDER BY cycle.cycle_number DESC))[1],
		  MAX(cycle.cycle_number),
		  COUNT(*)::integer,
		  MIN(cycle.finished_at),
		  MAX(cycle.finished_at)
		FROM detective_mcp_read_collection_cycles AS cycle
		JOIN source_snapshots AS snapshot
		  ON snapshot.source_snapshot_id = cycle.source_snapshot_id
		WHERE cycle.source_binding_id = $1
		  AND cycle.status = 'completed'
		  AND ($2 = '' OR cycle.provider_revision = $2)
		GROUP BY
		  cycle.source_snapshot_id,
		  snapshot.source_system,
		  snapshot.source_id,
		  snapshot.source_version,
		  snapshot.raw_content_hash,
		  cycle.provider_object_id,
		  cycle.provider_revision,
		  cycle.document_id,
		  cycle.coverage_complete,
		  cycle.coverage_truncated,
		  cycle.completion_reason
		ORDER BY MAX(cycle.cycle_number) DESC
		LIMIT $3
	`, sourceBindingID, revision, limit)
	if err != nil {
		return nil, fmt.Errorf("querying MCP source states: %w", err)
	}
	defer rows.Close()

	states := make([]MCPReadSourceState, 0, limit)
	for rows.Next() {
		var state MCPReadSourceState
		if err := rows.Scan(
			&state.SourceSnapshotID,
			&state.SourceSystem,
			&state.SourceID,
			&state.SourceVersion,
			&state.RawContentHash,
			&state.ProviderObjectID,
			&state.ProviderRevision,
			&state.DocumentID,
			&state.CoverageComplete,
			&state.CoverageTruncated,
			&state.CompletionReason,
			&state.FirstObservedCycleID,
			&state.FirstObservedCycle,
			&state.LastObservedCycleID,
			&state.LastObservedCycle,
			&state.ObservationCount,
			&state.FirstObservedAt,
			&state.LastObservedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning MCP source state: %w", err)
		}
		state.FirstObservedAt = state.FirstObservedAt.UTC()
		state.LastObservedAt = state.LastObservedAt.UTC()
		state.ExactEvidenceFilter = MCPReadSourceStateEvidenceFilter{
			SourceSnapshotID: state.SourceSnapshotID,
			SourceID:         state.SourceID,
			SourceVersion:    state.SourceVersion,
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating MCP source states: %w", err)
	}
	return states, nil
}

func mcpReadSourceStateRef(state MCPReadSourceState) MCPReadSourceStateRef {
	return MCPReadSourceStateRef{
		SourceSnapshotID: state.SourceSnapshotID,
		ProviderRevision: state.ProviderRevision,
		RawContentHash:   state.RawContentHash,
	}
}
