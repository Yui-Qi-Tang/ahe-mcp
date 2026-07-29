package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	proposalSearchMaxRunes  = 256
	proposalSearchMaxTerms  = 16
	relationSymbolMaxRunes  = 2048
	defaultQueryResultLimit = 20
	maxQueryResultLimit     = 100
)

// SearchProposalRecords performs bounded PostgreSQL full-text search over persisted proposal statements.
func SearchProposalRecords(ctx context.Context, pool *pgxpool.Pool, input ProposalSearchInput) ([]ProposalSearchResult, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return searchProposalRecords(ctx, pgxDB{pool: pool}, input)
}

func searchProposalRecords(ctx context.Context, db sqlDB, input ProposalSearchInput) ([]ProposalSearchResult, error) {
	input.Query = strings.TrimSpace(input.Query)
	input.ProposalListInput = normalizeProposalListInput(input.ProposalListInput)
	if err := validateProposalSearchInput(input); err != nil {
		return nil, err
	}

	var results []ProposalSearchResult
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		var queryErr error
		results, queryErr = queryProposalSearchResults(ctx, tx, input, input.Limit)
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func queryProposalSearchResults(ctx context.Context, db sqlQueryer, input ProposalSearchInput, limit int) ([]ProposalSearchResult, error) {
	rows, err := db.query(ctx, `
		SELECT
			po.proposal_occurrence_id,
			ts_rank_cd(to_tsvector('simple', po.statement_text), plainto_tsquery('simple', $1))::double precision
		FROM proposal_occurrences po
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = po.extraction_attempt_id
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		LEFT JOIN source_snapshots ss ON ss.source_snapshot_id = er.source_snapshot_id
		LEFT JOIN repository_snapshots rs ON rs.repository_snapshot_id = er.repository_snapshot_id
		LEFT JOIN repository_source_generations rsg ON rsg.proposal_batch_id = po.proposal_batch_id
		LEFT JOIN repository_source_heads rsh
			ON rsh.repo_id = rsg.repo_id
			AND rsh.extractor_name = rsg.extractor_name
			AND rsh.active_generation_id = rsg.source_generation_id
		WHERE to_tsvector('simple', po.statement_text) @@ plainto_tsquery('simple', $1)
			AND ($2 = '' OR ss.source_snapshot_id = $2)
			AND ($3 = '' OR rs.repository_snapshot_id = $3)
			AND ($4 = '' OR ss.source_id = $4)
			AND ($5 = '' OR ss.source_version = $5)
			AND ($6 = '' OR po.admission_outcome = $6)
			AND ($7 = '' OR rsg.source_generation_id = $7)
			AND (
				$8 = 'all'
				OR ($8 = 'active' AND (er.repository_snapshot_id IS NULL OR rsh.active_generation_id IS NOT NULL))
				OR ($8 = 'historical' AND er.repository_snapshot_id IS NOT NULL AND rsg.source_generation_id IS NOT NULL AND rsh.active_generation_id IS NULL)
			)
		ORDER BY
			ts_rank_cd(to_tsvector('simple', po.statement_text), plainto_tsquery('simple', $1)) DESC,
			po.created_at DESC,
			po.proposal_occurrence_id
		LIMIT $9
	`,
		input.Query,
		input.SourceSnapshotID,
		input.RepositorySnapshotID,
		input.SourceID,
		input.SourceVersion,
		input.AdmissionOutcome,
		input.SourceGenerationID,
		input.LifecycleScope,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("searching proposal records: %w", err)
	}
	return collectProposalSearchResults(ctx, db, rows)
}

func collectProposalSearchResults(ctx context.Context, db sqlQueryer, rows sqlRows) ([]ProposalSearchResult, error) {
	type rankedID struct {
		id   string
		rank float64
	}
	var ids []rankedID
	for rows.Next() {
		var item rankedID
		if err := rows.Scan(&item.id, &item.rank); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning proposal search result: %w", err)
		}
		ids = append(ids, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating proposal search results: %w", err)
	}
	rows.Close()

	results := make([]ProposalSearchResult, 0, len(ids))
	for _, item := range ids {
		record, err := getProposalByOccurrenceID(ctx, db, item.id)
		if err != nil {
			return nil, err
		}
		results = append(results, ProposalSearchResult{Record: record, Rank: item.rank})
	}
	return results, nil
}

func validateProposalSearchInput(input ProposalSearchInput) error {
	if input.Query == "" {
		return newDomainError(ErrorInvalidInput, "query is required")
	}
	if !utf8.ValidString(input.Query) || utf8.RuneCountInString(input.Query) > proposalSearchMaxRunes {
		return newDomainError(ErrorInvalidInput, "query must contain at most %d UTF-8 characters", proposalSearchMaxRunes)
	}
	terms := strings.Fields(input.Query)
	if len(terms) > proposalSearchMaxTerms {
		return newDomainError(ErrorInvalidInput, "query must contain at most %d terms", proposalSearchMaxTerms)
	}
	hasSearchRune := false
	for _, r := range input.Query {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			hasSearchRune = true
			break
		}
	}
	if !hasSearchRune {
		return newDomainError(ErrorInvalidInput, "query must contain at least one letter or number")
	}
	return validateProposalListInput(input.ProposalListInput)
}

// GetCanonicalRelationByID returns one canonical edge and its complete grounded provenance.
func GetCanonicalRelationByID(ctx context.Context, pool *pgxpool.Pool, canonicalEdgeID string) (CanonicalRelationQueryResult, error) {
	if pool == nil {
		return CanonicalRelationQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return getCanonicalRelationByID(ctx, pgxDB{pool: pool}, canonicalEdgeID)
}

func getCanonicalRelationByID(ctx context.Context, db sqlQueryer, canonicalEdgeID string) (CanonicalRelationQueryResult, error) {
	canonicalEdgeID = strings.TrimSpace(canonicalEdgeID)
	if canonicalEdgeID == "" {
		return CanonicalRelationQueryResult{}, newDomainError(ErrorInvalidInput, "canonical_edge_id is required")
	}
	if !strings.HasPrefix(canonicalEdgeID, "canon-edge:") {
		return CanonicalRelationQueryResult{}, newDomainError(ErrorInvalidRecordID, "canonical_edge_id %q must start with canon-edge:", canonicalEdgeID)
	}

	edge, err := loadCanonicalEdge(ctx, db, canonicalEdgeID)
	if err != nil {
		return CanonicalRelationQueryResult{}, err
	}
	from, err := getCanonicalEvidenceByID(ctx, db, edge.From)
	if err != nil {
		return CanonicalRelationQueryResult{}, err
	}
	to, err := getCanonicalEvidenceByID(ctx, db, edge.To)
	if err != nil {
		return CanonicalRelationQueryResult{}, err
	}
	origin, err := traceProposalProvenance(ctx, db, edge.OriginProposalOccurrenceID)
	if err != nil {
		return CanonicalRelationQueryResult{}, err
	}
	return CanonicalRelationQueryResult{Edge: edge, From: from, To: to, OriginProposal: origin}, nil
}

func loadCanonicalEdge(ctx context.Context, db sqlQueryer, canonicalEdgeID string) (CanonicalGraphEdge, error) {
	var edge CanonicalGraphEdge
	var relation string
	var provenanceData []byte
	err := db.queryRow(ctx, `
		SELECT canonical_edge_id, from_node_id, to_node_id, relation, provenance, origin_proposal_occurrence_id
		FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
	`, canonicalEdgeID).Scan(
		&edge.ID,
		&edge.From,
		&edge.To,
		&relation,
		&provenanceData,
		&edge.OriginProposalOccurrenceID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CanonicalGraphEdge{}, newDomainError(ErrorMissingSourceViewAttempt, "canonical relation %s not found", canonicalEdgeID)
		}
		return CanonicalGraphEdge{}, fmt.Errorf("querying canonical relation: %w", err)
	}
	edge.Relation = evidencegraph.CanonicalEdgeRelation(relation)
	if err := json.Unmarshal(provenanceData, &edge.Provenance); err != nil {
		return CanonicalGraphEdge{}, fmt.Errorf("decoding canonical relation provenance: %w", err)
	}
	return edge, nil
}

// ListCanonicalNeighbors returns a bounded one-hop canonical neighborhood.
func ListCanonicalNeighbors(ctx context.Context, pool *pgxpool.Pool, input CanonicalNeighborInput) ([]CanonicalNeighborResult, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return listCanonicalNeighbors(ctx, pgxDB{pool: pool}, input)
}

func listCanonicalNeighbors(ctx context.Context, db sqlDB, input CanonicalNeighborInput) ([]CanonicalNeighborResult, error) {
	input = normalizeCanonicalNeighborInput(input)
	if err := validateCanonicalNeighborInput(input); err != nil {
		return nil, err
	}

	var results []CanonicalNeighborResult
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		if _, err := getCanonicalEvidenceByID(ctx, tx, input.CanonicalID); err != nil {
			return err
		}
		rows, err := tx.query(ctx, `
			SELECT canonical_edge_id
			FROM canonical_graph_edges
			WHERE (
				($2 = 'both' AND (from_node_id = $1 OR to_node_id = $1))
				OR ($2 = 'outgoing' AND from_node_id = $1)
				OR ($2 = 'incoming' AND to_node_id = $1)
			)
				AND ($3 = '' OR relation = $3)
			ORDER BY created_at, canonical_edge_id
			LIMIT $4
		`, input.CanonicalID, input.Direction, input.Relation, input.Limit)
		if err != nil {
			return fmt.Errorf("querying canonical neighbors: %w", err)
		}
		var edgeIDs []string
		for rows.Next() {
			var edgeID string
			if err := rows.Scan(&edgeID); err != nil {
				rows.Close()
				return fmt.Errorf("scanning canonical neighbor: %w", err)
			}
			edgeIDs = append(edgeIDs, edgeID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterating canonical neighbors: %w", err)
		}
		rows.Close()

		results = make([]CanonicalNeighborResult, 0, len(edgeIDs))
		for _, edgeID := range edgeIDs {
			relation, err := getCanonicalRelationByID(ctx, tx, edgeID)
			if err != nil {
				return err
			}
			neighbor := relation.To
			var directions []string
			if relation.Edge.From == input.CanonicalID {
				directions = append(directions, RelationDirectionOutgoing)
			}
			if relation.Edge.To == input.CanonicalID {
				directions = append(directions, RelationDirectionIncoming)
				if relation.Edge.From != input.CanonicalID {
					neighbor = relation.From
				}
			}
			results = append(results, CanonicalNeighborResult{Directions: directions, Relation: relation, Neighbor: neighbor})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// ListRepositoryRelationNeighbors returns bounded one-hop typed repository relations for an exact symbol.
func ListRepositoryRelationNeighbors(ctx context.Context, pool *pgxpool.Pool, input RepositoryRelationNeighborInput) ([]RepositoryRelationNeighborResult, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return listRepositoryRelationNeighbors(ctx, pgxDB{pool: pool}, input)
}

func listRepositoryRelationNeighbors(ctx context.Context, db sqlDB, input RepositoryRelationNeighborInput) ([]RepositoryRelationNeighborResult, error) {
	input = normalizeRepositoryRelationNeighborInput(input)
	if err := validateRepositoryRelationNeighborInput(input); err != nil {
		return nil, err
	}

	var results []RepositoryRelationNeighborResult
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		rows, err := tx.query(ctx, `
			SELECT po.proposal_occurrence_id
			FROM proposal_occurrences po
			JOIN extraction_attempts ea ON ea.extraction_attempt_id = po.extraction_attempt_id
			JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
			JOIN repository_source_generations rsg ON rsg.proposal_batch_id = po.proposal_batch_id
			LEFT JOIN repository_source_heads rsh
				ON rsh.repo_id = rsg.repo_id
				AND rsh.extractor_name = rsg.extractor_name
				AND rsh.active_generation_id = rsg.source_generation_id
			WHERE po.proposed_payload ? 'code_relation'
				AND (
					($2 = 'both' AND (
						po.proposed_payload->'code_relation'->'caller'->>'symbol_ref' = $1
						OR po.proposed_payload->'code_relation'->'target'->>'symbol_ref' = $1
					))
					OR ($2 = 'outgoing' AND po.proposed_payload->'code_relation'->'caller'->>'symbol_ref' = $1)
					OR ($2 = 'incoming' AND po.proposed_payload->'code_relation'->'target'->>'symbol_ref' = $1)
				)
				AND ($3 = '' OR po.proposed_payload->'code_relation'->>'relation_kind' = $3)
				AND ($4 = '' OR rsg.source_generation_id = $4)
				AND (
					$5 = 'all'
					OR ($5 = 'active' AND rsh.active_generation_id IS NOT NULL)
					OR ($5 = 'historical' AND rsh.active_generation_id IS NULL)
				)
			ORDER BY po.created_at DESC, po.proposal_occurrence_id
			LIMIT $6
		`, input.SymbolRef, input.Direction, input.RelationKind, input.SourceGenerationID, input.LifecycleScope, input.Limit)
		if err != nil {
			return fmt.Errorf("querying repository relation neighbors: %w", err)
		}
		var occurrenceIDs []string
		for rows.Next() {
			var occurrenceID string
			if err := rows.Scan(&occurrenceID); err != nil {
				rows.Close()
				return fmt.Errorf("scanning repository relation neighbor: %w", err)
			}
			occurrenceIDs = append(occurrenceIDs, occurrenceID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterating repository relation neighbors: %w", err)
		}
		rows.Close()

		results = make([]RepositoryRelationNeighborResult, 0, len(occurrenceIDs))
		for _, occurrenceID := range occurrenceIDs {
			record, err := getProposalByOccurrenceID(ctx, tx, occurrenceID)
			if err != nil {
				return err
			}
			neighbor, err := repositoryNeighborResult(input.SymbolRef, record)
			if err != nil {
				return err
			}
			results = append(results, neighbor)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func repositoryNeighborResult(symbolRef string, record ProposalQueryResult) (RepositoryRelationNeighborResult, error) {
	relation := record.CodeRelation
	if relation == nil {
		return RepositoryRelationNeighborResult{}, newDomainError(ErrorRepositorySnapshotIntegrity, "proposal %s has no typed code relation", record.ProposalOccurrenceID)
	}
	result := RepositoryRelationNeighborResult{Relation: record}
	matchesCaller := relation.Caller != nil && relation.Caller.SymbolRef == symbolRef
	matchesTarget := relation.Target.SymbolRef == symbolRef
	if matchesCaller {
		result.Directions = append(result.Directions, RelationDirectionOutgoing)
		declaration := relation.Target
		result.NeighborDeclaration = &declaration
	}
	if matchesTarget {
		result.Directions = append(result.Directions, RelationDirectionIncoming)
		if !matchesCaller {
			if relation.Caller != nil {
				declaration := *relation.Caller
				result.NeighborDeclaration = &declaration
			} else {
				usage := relation.Usage
				result.NeighborUsage = &usage
			}
		}
	}
	if len(result.Directions) == 0 {
		return RepositoryRelationNeighborResult{}, newDomainError(ErrorRepositorySnapshotIntegrity, "proposal %s does not reference symbol %s", record.ProposalOccurrenceID, symbolRef)
	}
	return result, nil
}

func normalizeCanonicalNeighborInput(input CanonicalNeighborInput) CanonicalNeighborInput {
	input.CanonicalID = strings.TrimSpace(input.CanonicalID)
	input.Direction = strings.TrimSpace(input.Direction)
	input.Relation = strings.TrimSpace(input.Relation)
	if input.Direction == "" {
		input.Direction = RelationDirectionBoth
	}
	if input.Limit == 0 {
		input.Limit = defaultQueryResultLimit
	}
	return input
}

func validateCanonicalNeighborInput(input CanonicalNeighborInput) error {
	if input.CanonicalID == "" {
		return newDomainError(ErrorInvalidInput, "canonical_id is required")
	}
	if !strings.HasPrefix(input.CanonicalID, "canon-node:") {
		return newDomainError(ErrorInvalidRecordID, "canonical_id %q must start with canon-node:", input.CanonicalID)
	}
	if err := validateRelationDirection(input.Direction); err != nil {
		return err
	}
	if input.Relation != "" {
		valid := false
		for _, relation := range evidencegraph.CanonicalRelations() {
			if input.Relation == string(relation) {
				valid = true
				break
			}
		}
		if !valid {
			return newDomainError(ErrorInvalidInput, "canonical relation %q is not supported", input.Relation)
		}
	}
	return validateQueryLimit(input.Limit)
}

func normalizeRepositoryRelationNeighborInput(input RepositoryRelationNeighborInput) RepositoryRelationNeighborInput {
	input.SymbolRef = strings.TrimSpace(input.SymbolRef)
	input.Direction = strings.TrimSpace(input.Direction)
	input.RelationKind = strings.TrimSpace(input.RelationKind)
	input.LifecycleScope = strings.TrimSpace(input.LifecycleScope)
	input.SourceGenerationID = strings.TrimSpace(input.SourceGenerationID)
	if input.Direction == "" {
		input.Direction = RelationDirectionBoth
	}
	if input.LifecycleScope == "" {
		input.LifecycleScope = ProposalLifecycleScopeActive
		if input.SourceGenerationID != "" {
			input.LifecycleScope = ProposalLifecycleScopeAll
		}
	}
	if input.Limit == 0 {
		input.Limit = defaultQueryResultLimit
	}
	return input
}

func validateRepositoryRelationNeighborInput(input RepositoryRelationNeighborInput) error {
	if input.SymbolRef == "" {
		return newDomainError(ErrorInvalidInput, "symbol_ref is required")
	}
	if !strings.HasPrefix(input.SymbolRef, "symbol:") || utf8.RuneCountInString(input.SymbolRef) > relationSymbolMaxRunes {
		return newDomainError(ErrorInvalidRecordID, "symbol_ref must start with symbol: and contain at most %d UTF-8 characters", relationSymbolMaxRunes)
	}
	if err := validateRelationDirection(input.Direction); err != nil {
		return err
	}
	switch input.RelationKind {
	case "", CodeRelationKindDefinition, CodeRelationKindCall:
	default:
		return newDomainError(ErrorInvalidInput, "relation_kind %q is not supported", input.RelationKind)
	}
	if input.SourceGenerationID != "" && !strings.HasPrefix(input.SourceGenerationID, "generation:") {
		return newDomainError(ErrorInvalidRecordID, "source_generation_id %q must start with generation:", input.SourceGenerationID)
	}
	if err := validateProposalLifecycleScope(input.LifecycleScope); err != nil {
		return err
	}
	return validateQueryLimit(input.Limit)
}

func validateRelationDirection(direction string) error {
	switch direction {
	case RelationDirectionIncoming, RelationDirectionOutgoing, RelationDirectionBoth:
		return nil
	default:
		return newDomainError(ErrorInvalidInput, "direction %q is not supported", direction)
	}
}

func validateQueryLimit(limit int) error {
	if limit < 1 {
		return newDomainError(ErrorInvalidInput, "limit must be at least 1")
	}
	if limit > maxQueryResultLimit {
		return newDomainError(ErrorInvalidInput, "limit must be at most %d", maxQueryResultLimit)
	}
	return nil
}

func withReadOnlyTx(ctx context.Context, db sqlDB, fn func(sqlTx) error) error {
	tx, err := db.begin(ctx)
	if err != nil {
		return fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer func() {
		_ = tx.rollback(context.Background())
	}()
	if _, err := tx.exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`); err != nil {
		return fmt.Errorf("configuring read-only transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.commit(ctx); err != nil {
		return fmt.Errorf("commit read-only transaction: %w", err)
	}
	return nil
}
