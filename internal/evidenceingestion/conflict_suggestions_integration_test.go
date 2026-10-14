//go:build integration

package evidenceingestion

import (
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
)

func TestIntegrationConflictSuggestionsReadOnlyAndBounded(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	_, current := prepareIntegrationClosurePair(t, ctx, pool, "suggestions")
	record, err := GetCanonicalEvidenceByID(ctx, pool, current.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	input := ConflictSuggestionRequest{ProposalOccurrenceID: record.OriginProposalOccurrenceID, ScanLimit: 1000, Limit: 100}
	before, err := ReadEvidenceUseProjection(ctx, pool, []string{current.CanonicalRef})
	if err != nil {
		t.Fatal(err)
	}
	reader := externalRecordRolePool(t, ctx, pool, dbrole.ProfileIntake)
	result, err := SuggestIntakeConflicts(ctx, reader, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Suggestions) != 1 || result.Coverage.ScanTruncated || !result.Coverage.ObjectIdentityAvailable || !result.Coverage.RelationAnchorAvailable || result.Coverage.SlotBasis != "recorded_membership" {
		t.Fatal("missing bounded matching evidence", result)
	}
	kinds := map[string]bool{}
	for _, m := range result.Suggestions[0].Matches {
		kinds[m.Kind] = true
		if m.Edge != nil && (m.Edge.From != current.CanonicalRef || m.Edge.Relation != "supersedes" || len(m.Edge.Provenance) == 0) {
			t.Fatal("inferred or directionless edge")
		}
	}
	for _, kind := range []string{"same_external_object", "same_declared_slot", "existing_direct_relation", "lexical_overlap"} {
		if !kinds[kind] {
			t.Fatal("missing route", kind)
		}
	}
	after, err := ReadEvidenceUseProjection(ctx, pool, []string{current.CanonicalRef})
	if err != nil || !reflect.DeepEqual(before.Snapshot, after.Snapshot) {
		t.Fatal("suggestions changed governance or graph", err)
	}
	pending := createSupersessionExternalProposal(t, ctx, pool, "suggestions-pending", "r3", "Policy value is v3.")
	basis := supersessionIntegrationBasis()
	input = ConflictSuggestionRequest{ProposalOccurrenceID: pending.ProposalOccurrenceID, Basis: &basis, ScanLimit: 1000, Limit: 100}
	result, err = SuggestIntakeConflicts(ctx, reader, input)
	if err != nil || len(result.Suggestions) != 2 || result.Coverage.RelationAnchorAvailable || result.Coverage.SlotBasis != "caller_declared_hint" {
		t.Fatal("pending slot hint", err, result.Coverage)
	}
	for _, c := range result.Suggestions {
		for _, m := range c.Matches {
			if m.Edge != nil {
				t.Fatal("invented edge for pending proposal")
			}
		}
	}
	input.Limit = 1
	result, err = SuggestIntakeConflicts(ctx, reader, input)
	if err != nil || !result.Coverage.ResultsTruncated || result.Coverage.MatchedClaims != 2 {
		t.Fatal("return truncation hidden", err)
	}
	input.ScanLimit = 1
	result, err = SuggestIntakeConflicts(ctx, reader, input)
	if err != nil || !result.Coverage.ScanTruncated || result.Coverage.ScannedClaims != 1 {
		t.Fatal("scan truncation hidden", err)
	}
	// An unrelated source cannot be pulled in by a caller-supplied foreign slot.
	bad := basis
	bad.ObjectID = "other"
	input.Basis = &bad
	if _, err = SuggestIntakeConflicts(ctx, reader, input); err == nil {
		t.Fatal("foreign slot hint accepted")
	}
	empty, err := IngestManualText(ctx, pool, ManualTextInput{SourceID: "unrelated", SourceVersion: "1", Raw: []byte("Zebra quantum."), RequestID: "unrelated"}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: "Zebra quantum.", EvidenceRefs: []string{"span:S1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err = SuggestIntakeConflicts(ctx, reader, ConflictSuggestionRequest{ProposalOccurrenceID: empty.ProposalOccurrenceID})
	if err != nil || len(result.Suggestions) != 0 || len(result.Limitations) == 0 || result.Coverage.ObjectIdentityAvailable {
		t.Fatal("empty scope represented as global clearance", err)
	}
	// Partial visibility must fail, not silently masquerade as a clean search.
	if _, err = pool.Exec(ctx, `ALTER TABLE canonical_graph_nodes ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err = SuggestIntakeConflicts(ctx, reader, ConflictSuggestionRequest{ProposalOccurrenceID: empty.ProposalOccurrenceID}); err == nil {
		t.Fatal("inaccessible candidates silently omitted")
	}
}
