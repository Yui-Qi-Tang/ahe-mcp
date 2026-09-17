package evidenceingestion

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5"
)

// LoadReferencesNativeEndpointsInTx reconstructs immutable endpoint authority.
// It deliberately does not inspect today's target candidates: historical exact
// replay uses its retained observation without reinterpreting later admissions.
func LoadReferencesNativeEndpointsInTx(ctx context.Context, tx pgx.Tx, fromID, toID string) (ReferencesNativeBasis, error) {
	if tx == nil {
		return ReferencesNativeBasis{}, newDomainError(ErrorInvalidInput, "postgres transaction is required")
	}
	return loadReferencesNativeEndpoints(ctx, pgxTx{tx: tx}, fromID, toID)
}

// LoadReferencesNativeBasisInTx adds the complete target-overlap set observed in
// the caller's repeatable-read or serializable snapshot. The caller must set
// row_security=off; filtered rows cannot establish unique endpoint authority.
func LoadReferencesNativeBasisInTx(ctx context.Context, tx pgx.Tx, fromID, toID string) (ReferencesNativeBasis, error) {
	if tx == nil {
		return ReferencesNativeBasis{}, newDomainError(ErrorInvalidInput, "postgres transaction is required")
	}
	basis, err := loadReferencesNativeEndpoints(ctx, pgxTx{tx: tx}, fromID, toID)
	if err != nil {
		return ReferencesNativeBasis{}, err
	}
	basis.TargetCandidateIDs, err = loadReferencesTargetCandidates(ctx, pgxTx{tx: tx}, basis.To)
	if err != nil {
		return ReferencesNativeBasis{}, err
	}
	return basis, nil
}

func loadReferencesNativeEndpoints(ctx context.Context, tx sqlTx, fromID, toID string) (ReferencesNativeBasis, error) {
	if err := ctx.Err(); err != nil {
		return ReferencesNativeBasis{}, err
	}
	if err := validateDerivedImplementsCoordinates(fromID, toID); err != nil {
		return ReferencesNativeBasis{}, err
	}
	if err := requireReferencesNativeTx(ctx, tx); err != nil {
		return ReferencesNativeBasis{}, err
	}
	from, err := loadReferencesNativeEndpoint(ctx, tx, fromID)
	if err != nil {
		return ReferencesNativeBasis{}, err
	}
	to, err := loadReferencesNativeEndpoint(ctx, tx, toID)
	if err != nil {
		return ReferencesNativeBasis{}, err
	}
	if _, err := referencesTargetSpan(to); err != nil {
		return ReferencesNativeBasis{}, err
	}
	return ReferencesNativeBasis{From: from, To: to}, nil
}

func requireReferencesNativeTx(ctx context.Context, tx sqlQueryer) error {
	var isolation, rowSecurity string
	if err := tx.queryRow(ctx, `SELECT current_setting('transaction_isolation'),current_setting('row_security')`).Scan(&isolation, &rowSecurity); err != nil {
		return fmt.Errorf("reading references transaction policy: %w", err)
	}
	if (isolation != "repeatable read" && isolation != "serializable") || rowSecurity != "off" {
		return newDomainError(ErrorInvalidInput, "references require repeatable-read or serializable isolation and row_security=off")
	}
	return nil
}

func loadReferencesNativeEndpoint(ctx context.Context, tx sqlTx, nodeID string) (ReferencesNativeEndpoint, error) {
	node, _, err := loadDerivedImplementsAdmittedNode(ctx, tx, nodeID, evidencegraph.CanonicalSourceClaim)
	if err != nil {
		return ReferencesNativeEndpoint{}, err
	}
	p := node.OriginProposal
	if p.SourceBindingKind != ProposalSourceBindingSourceSnapshot || (p.SourceSystem != SourceSystemExternalDocument && p.SourceSystem != SourceSystemManualText) {
		return ReferencesNativeEndpoint{}, newDomainError(ErrorUnsupportedAdmission, "references require qualified manual or external source claims")
	}
	view, err := loadBoundedSourceViewFromQueryer(ctx, tx, p.SourceSnapshotID, p.ExtractionViewID)
	if err != nil {
		return ReferencesNativeEndpoint{}, err
	}
	if view.Status != BoundedSourceViewStatusAvailable || view.Input == nil {
		return ReferencesNativeEndpoint{}, newDomainError(ErrorInvalidInput, "references source view exceeds bounded materialization limits")
	}
	leaf, err := loadDerivedImplementsSourceLeaf(ctx, tx, node)
	if err != nil {
		return ReferencesNativeEndpoint{}, err
	}
	spans, err := validateReferencesNativeSource(p, leaf, *view.Input)
	if err != nil {
		return ReferencesNativeEndpoint{}, err
	}
	return ReferencesNativeEndpoint{Node: node, ReviewSnapshot: leaf.ReviewSnapshot,
		RawText: leaf.RawText, RenderedText: leaf.RenderedText, Spans: spans}, nil
}

func validateReferencesNativeSource(p ProposalQueryResult, leaf DerivedImplementsSourceLeaf, input ExtractorInput) ([]SpanEntry, error) {
	expectedSource, expectedView, expectedSpans, err := buildManualSource(ManualTextInput{
		SourceSystem: p.SourceSystem, SourceID: p.SourceID, SourceVersion: p.SourceVersion,
		Raw: []byte(leaf.RawText), OriginMetadata: p.OriginMetadata,
	})
	if err != nil {
		return nil, err
	}
	if expectedSource.ID != p.SourceSnapshotID || expectedSource.RawContentHash != p.RawContentHash ||
		expectedView.ID != p.ExtractionViewID || string(expectedView.Rendered) != leaf.RenderedText ||
		input.SourceSnapshotID != expectedSource.ID || input.SourceID != expectedSource.SourceID || input.SourceVersion != expectedSource.SourceVersion ||
		input.SourceSystem != expectedSource.SourceSystem || input.RawContentHash != expectedSource.RawContentHash ||
		input.ExtractionViewID != expectedView.ID || input.Renderer != (RendererRef{Name: expectedView.RendererName, Version: expectedView.RendererVersion}) ||
		input.RenderedContentHash != expectedView.RenderedContentHash || input.RenderedText != leaf.RenderedText ||
		len(input.Spans) != len(expectedSpans) {
		return nil, newDomainError(ErrorReviewContractConflict, "references immutable source/view differs from original bytes")
	}
	for i, span := range expectedSpans {
		actual := input.Spans[i]
		if input.SpanCatalogVersion != span.SpanCatalogVersion || actual.SpanID != span.SpanID || actual.StartByte != span.StartByte ||
			actual.EndByte != span.EndByte || actual.DisplayLine != span.DisplayLine || actual.QuotedTextHash != span.QuotedTextHash || actual.Text != span.QuotedText {
			return nil, newDomainError(ErrorReviewContractConflict, "references complete native span catalog differs from original bytes")
		}
	}
	return expectedSpans, nil
}

func referencesTargetSpan(target ReferencesNativeEndpoint) (SpanEntry, error) {
	refs := target.Node.OriginProposal.SourceRefs
	if len(refs) != 1 || !reflect.DeepEqual(refs, target.ReviewSnapshot.ReviewPackage.ProposalBasis.SourceRefs) {
		return SpanEntry{}, newDomainError(ErrorReviewContractConflict, "references target requires exactly one original source span")
	}
	for _, span := range target.Spans {
		if refs[0] == (ResolvedSourceRef{ExtractionViewID: span.ExtractionViewID, SpanID: span.SpanID,
			StartByte: span.StartByte, EndByte: span.EndByte, QuotedTextHash: span.QuotedTextHash, QuotedText: span.QuotedText}) {
			return span, nil
		}
	}
	return SpanEntry{}, newDomainError(ErrorReviewContractConflict, "references target differs from its complete native line span")
}

// Each query uses the same full native universe. In particular, there is no
// extractor-manifest, reviewer, source-profile or convenient-origin filter.
const referencesTargetCandidatesSQL = `WITH candidates AS (
	SELECT DISTINCT n.canonical_node_id
	FROM canonical_graph_nodes n
	JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
	JOIN extraction_attempts a ON a.extraction_attempt_id=p.extraction_attempt_id
	JOIN extraction_runs r ON r.extraction_run_id=a.extraction_run_id
	CROSS JOIN LATERAL jsonb_array_elements(p.source_refs) ref
	WHERE n.node_kind='source_claim' AND p.admission_outcome='admitted' AND p.canonical_ref=n.canonical_node_id
		AND r.source_snapshot_id=$1 AND r.extraction_view_id=$2
		AND ref->>'extraction_view_id'=$2
		AND (ref->>'start_byte')::bigint<$4 AND (ref->>'end_byte')::bigint>$3
) `

func loadReferencesTargetCandidates(ctx context.Context, tx sqlQueryer, target ReferencesNativeEndpoint) ([]string, error) {
	span, err := referencesTargetSpan(target)
	if err != nil {
		return nil, err
	}
	p := target.Node.OriginProposal
	args := []any{p.SourceSnapshotID, span.ExtractionViewID, span.StartByte, span.EndByte}
	var count int64
	if err := tx.queryRow(ctx, referencesTargetCandidatesSQL+`SELECT count(*) FROM candidates`, args...).Scan(&count); err != nil {
		return nil, fmt.Errorf("counting complete references target candidates: %w", err)
	}
	if count < 1 || count > ReferencesMaxTargetCandidates {
		return nil, newDomainError(ErrorReviewContractConflict, "references target candidate count is outside the complete bounded cut")
	}
	rows, err := tx.query(ctx, referencesTargetCandidatesSQL+`SELECT canonical_node_id FROM candidates ORDER BY canonical_node_id COLLATE "C"`, args...)
	if err != nil {
		return nil, fmt.Errorf("loading complete references target candidates: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, int(count))
	for rows.Next() {
		if len(ids) >= int(count) {
			return nil, newDomainError(ErrorReviewContractConflict, "references target candidates changed within observation")
		}
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning references target candidate: %w", err)
		}
		if !hasStableIDPrefix(id, "canon-node:") || len(id) > 512 || strings.TrimSpace(id) != id ||
			(len(ids) > 0 && ids[len(ids)-1] >= id) {
			return nil, newDomainError(ErrorReviewContractConflict, "references target candidates are not exact sorted distinct native IDs")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading references target candidate rows: %w", err)
	}
	if len(ids) != int(count) || !slices.Contains(ids, target.Node.CanonicalID) {
		return nil, newDomainError(ErrorReviewContractConflict, "references target candidate cut is incomplete")
	}
	return ids, nil
}
