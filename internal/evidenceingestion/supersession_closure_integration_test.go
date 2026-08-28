//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationCanonicalSupersessionCurrentnessProjectsChainBranchAndMerge(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()
	lineageKey := integrationClosureLineageKey(t, basis)

	v1Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-v1", "r1", "Policy value is v1.")
	v1, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: v1Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial closure fixture",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(v1) error = %v", err)
	}
	v1Temporal := canonicalTemporalJSON(t, ctx, pool, v1.CanonicalRef)

	v2Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-v2", "r2", "Policy value is v2.")
	v2, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v2Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v2 replaces v1",
		Basis:                basis,
		TargetNodeIDs:        []string{v1.CanonicalRef},
	})
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(v2) error = %v", err)
	}
	chain := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
	assertIntegrationClosureBinding(t, chain, v2.EventRevision, v2.AdmissionEventID, true)
	assertIntegrationClosureNodes(t, chain, map[string]integrationClosureExpectedNode{
		v1.CanonicalRef: {
			status:   evidencesupersession.CurrentnessSuperseded,
			incoming: []string{v2.SupersedesEdgeIDs[0]},
		},
		v2.CanonicalRef: {status: evidencesupersession.CurrentnessCurrent},
	})
	if !slices.Equal(chain.Projection.FrontierNodeIDs, []string{v2.CanonicalRef}) {
		t.Fatalf("chain frontiers = %v, want [%s]", chain.Projection.FrontierNodeIDs, v2.CanonicalRef)
	}
	assertCanonicalTemporalStatus(t, ctx, pool, v1.CanonicalRef, evidencegraph.TemporalUnknown)
	assertCanonicalTemporalStatus(t, ctx, pool, v2.CanonicalRef, evidencegraph.TemporalUnknown)

	v3Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-v3", "r3", "Policy branch value is v3.")
	v3, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v3Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v3 creates a reviewed branch from v1",
		Basis:                basis,
		TargetNodeIDs:        []string{v1.CanonicalRef},
		ExpectedRevision:     v2.EventRevision,
		ExpectedHeadEventID:  v2.AdmissionEventID,
	})
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(v3 branch) error = %v", err)
	}
	branch := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
	assertIntegrationClosureBinding(t, branch, v3.EventRevision, v3.AdmissionEventID, true)
	assertIntegrationClosureNodes(t, branch, map[string]integrationClosureExpectedNode{
		v1.CanonicalRef: {
			status:   evidencesupersession.CurrentnessSuperseded,
			incoming: sortedIntegrationClosureStrings(v2.SupersedesEdgeIDs[0], v3.SupersedesEdgeIDs[0]),
		},
		v2.CanonicalRef: {status: evidencesupersession.CurrentnessAmbiguous},
		v3.CanonicalRef: {status: evidencesupersession.CurrentnessAmbiguous},
	})
	wantBranchFrontiers := sortedIntegrationClosureStrings(v2.CanonicalRef, v3.CanonicalRef)
	if !slices.Equal(branch.Projection.FrontierNodeIDs, wantBranchFrontiers) {
		t.Fatalf("branch frontiers = %v, want %v", branch.Projection.FrontierNodeIDs, wantBranchFrontiers)
	}
	if branch.Projection.CutHash == chain.Projection.CutHash ||
		branch.Projection.ObjectClaimManifestHash == chain.Projection.ObjectClaimManifestHash {
		t.Fatalf("branch did not change cut/object manifest: chain=%+v branch=%+v", chain.Projection, branch.Projection)
	}

	v4Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-v4", "r4", "Merged policy value is v4.")
	v4, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v4Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v4 resolves the two reviewed branches",
		Basis:                basis,
		TargetNodeIDs:        []string{v2.CanonicalRef, v3.CanonicalRef},
		ExpectedRevision:     v3.EventRevision,
		ExpectedHeadEventID:  v3.AdmissionEventID,
	})
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(v4 merge) error = %v", err)
	}
	mergeEdgeByTarget := integrationClosureEdgeByTarget(t, v4)
	merged := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
	assertIntegrationClosureBinding(t, merged, v4.EventRevision, v4.AdmissionEventID, true)
	assertIntegrationClosureNodes(t, merged, map[string]integrationClosureExpectedNode{
		v1.CanonicalRef: {
			status:   evidencesupersession.CurrentnessSuperseded,
			incoming: sortedIntegrationClosureStrings(v2.SupersedesEdgeIDs[0], v3.SupersedesEdgeIDs[0]),
		},
		v2.CanonicalRef: {
			status:   evidencesupersession.CurrentnessSuperseded,
			incoming: []string{mergeEdgeByTarget[v2.CanonicalRef]},
		},
		v3.CanonicalRef: {
			status:   evidencesupersession.CurrentnessSuperseded,
			incoming: []string{mergeEdgeByTarget[v3.CanonicalRef]},
		},
		v4.CanonicalRef: {status: evidencesupersession.CurrentnessCurrent},
	})
	if !slices.Equal(merged.Projection.FrontierNodeIDs, []string{v4.CanonicalRef}) {
		t.Fatalf("merge frontiers = %v, want [%s]", merged.Projection.FrontierNodeIDs, v4.CanonicalRef)
	}
	if got := canonicalTemporalJSON(t, ctx, pool, v1.CanonicalRef); got != v1Temporal {
		t.Fatalf("v1 temporal changed from %s to %s", v1Temporal, got)
	}
	for _, nodeID := range []string{v2.CanonicalRef, v3.CanonicalRef, v4.CanonicalRef} {
		assertCanonicalTemporalStatus(t, ctx, pool, nodeID, evidencegraph.TemporalUnknown)
	}
}

func TestIntegrationCanonicalSupersessionCurrentnessAbstainsForUnclassifiedObjectClaim(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()
	lineageKey := integrationClosureLineageKey(t, basis)

	v1Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-partial-v1", "r1", "Policy value is v1.")
	v1, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: v1Proposal.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit partial v1: %v", err)
	}
	v2Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-partial-v2", "r2", "Policy value is v2.")
	v2, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v2Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v2 replaces v1",
		Basis:                basis,
		TargetNodeIDs:        []string{v1.CanonicalRef},
	})
	if err != nil {
		t.Fatalf("admit partial v2: %v", err)
	}
	complete := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
	assertIntegrationClosureBinding(t, complete, v2.EventRevision, v2.AdmissionEventID, true)

	unclassifiedProposal := createSupersessionExternalProposal(
		t,
		ctx,
		pool,
		"closure-partial-unclassified",
		"r3",
		"A separately admitted claim has no reviewed stable slot.",
	)
	unclassified, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: unclassifiedProposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "ordinary source claim without lineage classification",
	})
	if err != nil {
		t.Fatalf("admit unclassified source claim: %v", err)
	}

	partial := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
	assertIntegrationClosureBinding(t, partial, v2.EventRevision, v2.AdmissionEventID, false)
	assertIntegrationClosureNodes(t, partial, map[string]integrationClosureExpectedNode{
		v1.CanonicalRef: {
			status:   evidencesupersession.CurrentnessSuperseded,
			incoming: []string{v2.SupersedesEdgeIDs[0]},
		},
		v2.CanonicalRef: {status: evidencesupersession.CurrentnessUnknown},
	})
	if !slices.Equal(partial.Projection.FrontierNodeIDs, []string{v2.CanonicalRef}) {
		t.Fatalf("partial frontiers = %v, want [%s]", partial.Projection.FrontierNodeIDs, v2.CanonicalRef)
	}
	if !slices.Contains(partial.Limitations, canonicalSupersessionSnapshotLimitation) ||
		!slices.Contains(partial.Limitations, canonicalSupersessionUnclassifiedObjectClaimsLimitation) {
		t.Fatalf("partial limitations = %v", partial.Limitations)
	}
	if partial.Projection.Head != complete.Projection.Head ||
		partial.Projection.HistoryHash != complete.Projection.HistoryHash ||
		partial.Projection.CutHash != complete.Projection.CutHash {
		t.Fatalf("ordinary claim changed supersession head/history/cut: complete=%+v partial=%+v", complete.Projection, partial.Projection)
	}
	if partial.Projection.ObjectClaimManifestHash == complete.Projection.ObjectClaimManifestHash {
		t.Fatal("unclassified source claim did not change the source-object manifest")
	}
	assertCanonicalTemporalStatus(t, ctx, pool, unclassified.CanonicalRef, evidencegraph.TemporalUnknown)
}

func TestIntegrationCanonicalSupersessionCurrentnessTracksUnrelatedGlobalHead(t *testing.T) {
	ctx, pool := integrationPool(t)
	mainBasis := supersessionIntegrationBasis()
	mainLineageKey := integrationClosureLineageKey(t, mainBasis)

	mainV1Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-global-main-v1", "r1", "Main policy v1.")
	mainV1, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: mainV1Proposal.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit main v1: %v", err)
	}
	mainV2Proposal := createSupersessionExternalProposal(t, ctx, pool, "closure-global-main-v2", "r2", "Main policy v2.")
	mainV2, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: mainV2Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "main v2 replaces main v1",
		Basis:                mainBasis,
		TargetNodeIDs:        []string{mainV1.CanonicalRef},
	})
	if err != nil {
		t.Fatalf("admit main v2: %v", err)
	}
	before := integrationClosureCurrentnessReadOnly(t, ctx, pool, mainLineageKey)

	otherBasis := mainBasis
	otherBasis.ObjectID = "AHE-OTHER"
	otherV1Proposal := createIntegrationClosureExternalProposal(
		t, ctx, pool, "closure-global-other-v1", otherBasis.ObjectID, "r1", "Other policy v1.",
	)
	otherV1, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: otherV1Proposal.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit unrelated v1: %v", err)
	}
	otherV2Proposal := createIntegrationClosureExternalProposal(
		t, ctx, pool, "closure-global-other-v2", otherBasis.ObjectID, "r2", "Other policy v2.",
	)
	otherV2, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: otherV2Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "unrelated lineage advances the global head",
		Basis:                otherBasis,
		TargetNodeIDs:        []string{otherV1.CanonicalRef},
		ExpectedRevision:     mainV2.EventRevision,
		ExpectedHeadEventID:  mainV2.AdmissionEventID,
	})
	if err != nil {
		t.Fatalf("admit unrelated v2: %v", err)
	}

	after := integrationClosureCurrentnessReadOnly(t, ctx, pool, mainLineageKey)
	assertIntegrationClosureBinding(t, after, otherV2.EventRevision, otherV2.AdmissionEventID, true)
	if reflect.DeepEqual(after.Projection.Head, before.Projection.Head) ||
		after.Projection.HistoryHash == before.Projection.HistoryHash ||
		after.Projection.CutHash == before.Projection.CutHash {
		t.Fatalf("unrelated global event did not advance exact cut coordinate: before=%+v after=%+v", before.Projection, after.Projection)
	}
	if after.Projection.ObjectClaimManifestHash != before.Projection.ObjectClaimManifestHash {
		t.Fatalf("unrelated object changed selected object manifest: before=%s after=%s", before.Projection.ObjectClaimManifestHash, after.Projection.ObjectClaimManifestHash)
	}
	if !reflect.DeepEqual(after.Projection.Nodes, before.Projection.Nodes) ||
		!slices.Equal(after.Projection.FrontierNodeIDs, before.Projection.FrontierNodeIDs) {
		t.Fatalf("unrelated event changed selected topology: before=%+v after=%+v", before.Projection.Nodes, after.Projection.Nodes)
	}
}

func TestIntegrationCanonicalSupersessionCurrentnessRejectsTamperedEventPayload(t *testing.T) {
	ctx, pool := integrationPool(t)
	lineageKey, event := prepareIntegrationClosurePair(t, ctx, pool, "closure-tampered-event")

	// Bypass one guard in this isolated schema to prove the independent closure
	// validator still fails closed against privileged storage corruption.
	if _, err := pool.Exec(ctx, `
		ALTER TABLE canonical_supersession_admission_events
		DISABLE TRIGGER canonical_supersession_events_authority_trigger
	`); err != nil {
		t.Fatalf("disable isolated event authority trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE canonical_supersession_admission_events
		SET event_payload = jsonb_set(event_payload, '{kind}', '"forged"'::jsonb)
		WHERE event_id = $1
	`, event.AdmissionEventID); err != nil {
		t.Fatalf("tamper supersession event payload: %v", err)
	}
	assertIntegrationClosureCurrentnessFailsReadOnly(t, ctx, pool, lineageKey)
}

func TestIntegrationCanonicalSupersessionCurrentnessRejectsTamperedDecisionEdgeIDs(t *testing.T) {
	ctx, pool := integrationPool(t)
	lineageKey, event := prepareIntegrationClosurePair(t, ctx, pool, "closure-tampered-decision-edges")

	// Bypass one guard in this isolated schema to prove the independent closure
	// validator still fails closed against privileged storage corruption.
	if _, err := pool.Exec(ctx, `
		ALTER TABLE admission_decisions
		DISABLE TRIGGER canonical_supersession_decisions_authority_trigger
	`); err != nil {
		t.Fatalf("disable isolated decision authority trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE admission_decisions
		SET canonical_edge_ids = '[]'::jsonb
		WHERE admission_decision_id = $1
	`, event.AdmissionDecisionID); err != nil {
		t.Fatalf("tamper admission decision edge IDs: %v", err)
	}
	assertIntegrationClosureCurrentnessFailsReadOnly(t, ctx, pool, lineageKey)
}

func TestIntegrationCanonicalSupersessionCurrentnessRejectsMissingTargetMirror(t *testing.T) {
	ctx, pool := integrationPool(t)
	lineageKey, event := prepareIntegrationClosurePair(t, ctx, pool, "closure-missing-target")

	// integrationPool gives this test an isolated schema that is dropped during
	// cleanup. Disable only the scoped authority trigger so the loader can be
	// exercised against a committed privileged-corruption fixture.
	if _, err := pool.Exec(ctx, `
		ALTER TABLE canonical_supersession_replacement_targets
		DISABLE TRIGGER canonical_supersession_targets_authority_trigger
	`); err != nil {
		t.Fatalf("disable isolated target authority trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM canonical_supersession_replacement_targets
		WHERE event_id = $1
	`, event.AdmissionEventID); err != nil {
		t.Fatalf("delete isolated target mirror: %v", err)
	}
	assertIntegrationClosureCurrentnessFailsReadOnly(t, ctx, pool, lineageKey)
}

type integrationClosureExpectedNode struct {
	status   evidencesupersession.CurrentnessStatus
	incoming []string
}

func integrationClosureCurrentnessReadOnly(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	lineageKey string,
) CanonicalSupersessionCurrentness {
	t.Helper()
	beforeCounts := supersessionAuthorityCounts(t, ctx, pool)
	beforeHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head before currentness read: %v", err)
	}
	result, err := GetCanonicalSupersessionCurrentness(ctx, pool, lineageKey)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionCurrentness() error = %v", err)
	}
	afterCounts := supersessionAuthorityCounts(t, ctx, pool)
	afterHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head after currentness read: %v", err)
	}
	if beforeCounts != afterCounts || beforeHead != afterHead {
		t.Fatalf("currentness read mutated authority: counts %+v -> %+v, head %+v -> %+v", beforeCounts, afterCounts, beforeHead, afterHead)
	}
	return result
}

func assertIntegrationClosureCurrentnessFailsReadOnly(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	lineageKey string,
) {
	t.Helper()
	beforeCounts := supersessionAuthorityCounts(t, ctx, pool)
	beforeHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head before failed currentness read: %v", err)
	}
	_, err = GetCanonicalSupersessionCurrentness(ctx, pool, lineageKey)
	if err == nil {
		t.Fatal("GetCanonicalSupersessionCurrentness() error = nil, want fail-closed rejection")
	}
	assertKind(t, err, ErrorSupersessionInvariant)
	afterCounts := supersessionAuthorityCounts(t, ctx, pool)
	afterHead, headErr := GetCanonicalSupersessionHead(ctx, pool)
	if headErr != nil {
		t.Fatalf("load head after failed currentness read: %v", headErr)
	}
	if beforeCounts != afterCounts || beforeHead != afterHead {
		t.Fatalf("failed currentness read mutated authority: counts %+v -> %+v, head %+v -> %+v", beforeCounts, afterCounts, beforeHead, afterHead)
	}
}

func assertIntegrationClosureBinding(
	t *testing.T,
	result CanonicalSupersessionCurrentness,
	wantRevision int64,
	wantHeadEventID string,
	wantClosure bool,
) {
	t.Helper()
	projection := result.Projection
	if projection.LineageKey == "" ||
		projection.Head.ChainKey != evidencesupersession.ChainKey ||
		projection.Head.Revision != wantRevision ||
		projection.Head.HeadEventID != wantHeadEventID {
		t.Fatalf("projection coordinate = %+v, want revision/head %d/%s", projection, wantRevision, wantHeadEventID)
	}
	for name, value := range map[string]string{
		"history hash":         projection.HistoryHash,
		"cut hash":             projection.CutHash,
		"object manifest hash": projection.ObjectClaimManifestHash,
	} {
		if value == "" {
			t.Fatalf("projection %s is empty: %+v", name, projection)
		}
	}
	if projection.ClosureAvailable != wantClosure {
		t.Fatalf("ClosureAvailable = %t, want %t", projection.ClosureAvailable, wantClosure)
	}
	if !wantClosure {
		if projection.Witness != nil {
			t.Fatalf("projection without closure has witness %+v", projection.Witness)
		}
		return
	}
	if projection.Witness == nil {
		t.Fatal("projection with closure has no closure witness")
	}
	witness := projection.Witness
	if witness.Head != projection.Head ||
		witness.LineageKey != projection.LineageKey ||
		witness.HistoryHash != projection.HistoryHash ||
		witness.CutHash != projection.CutHash ||
		witness.ObjectClaimManifestHash != projection.ObjectClaimManifestHash ||
		!slices.Equal(witness.FrontierNodeIDs, projection.FrontierNodeIDs) ||
		witness.ID == "" {
		t.Fatalf("closure witness does not bind projection: projection=%+v witness=%+v", projection, witness)
	}
}

func assertIntegrationClosureNodes(
	t *testing.T,
	result CanonicalSupersessionCurrentness,
	want map[string]integrationClosureExpectedNode,
) {
	t.Helper()
	if len(result.Projection.Nodes) != len(want) {
		t.Fatalf("projection nodes = %+v, want %d nodes", result.Projection.Nodes, len(want))
	}
	seen := make(map[string]struct{}, len(result.Projection.Nodes))
	for _, node := range result.Projection.Nodes {
		expected, exists := want[node.NodeID]
		if !exists {
			t.Fatalf("unexpected projected node %+v", node)
		}
		if _, duplicate := seen[node.NodeID]; duplicate {
			t.Fatalf("duplicate projected node %s", node.NodeID)
		}
		seen[node.NodeID] = struct{}{}
		if node.Status != expected.status || !slices.Equal(node.IncomingEdgeIDs, expected.incoming) {
			t.Fatalf("projected node %s = status %s incoming %v, want %s/%v", node.NodeID, node.Status, node.IncomingEdgeIDs, expected.status, expected.incoming)
		}
	}
}

func prepareIntegrationClosurePair(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	prefix string,
) (string, SupersessionAdmissionResult) {
	t.Helper()
	basis := supersessionIntegrationBasis()
	v1Proposal := createSupersessionExternalProposal(t, ctx, pool, prefix+"-v1", "r1", "Policy value is v1.")
	v1, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: v1Proposal.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admit closure pair v1: %v", err)
	}
	v2Proposal := createSupersessionExternalProposal(t, ctx, pool, prefix+"-v2", "r2", "Policy value is v2.")
	v2, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v2Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v2 replaces v1",
		Basis:                basis,
		TargetNodeIDs:        []string{v1.CanonicalRef},
	})
	if err != nil {
		t.Fatalf("admit closure pair v2: %v", err)
	}
	return integrationClosureLineageKey(t, basis), v2
}

func createIntegrationClosureExternalProposal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	requestSuffix string,
	objectID string,
	revision string,
	statement string,
) IngestResult {
	t.Helper()
	envelope := validExternalSourceEnvelope("supersession-closure-" + requestSuffix)
	envelope.ObjectID = objectID
	envelope.Revision = revision
	envelope.SourceLocation = fmt.Sprintf("https://acme.example/jira/%s", objectID)
	envelope.Content = statement + "\n"
	envelope.ObservedAt = "2026-08-23T02:03:04Z"
	envelope.SourceUpdatedAt = ""
	source, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil {
		t.Fatalf("CaptureExternalSource(%s) error = %v", requestSuffix, err)
	}
	result, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID:        "supersession-closure-extractor-" + requestSuffix,
		SourceSnapshotID: source.SourceSnapshotID,
		ExtractionViewID: source.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{
			Name:    "supersession-closure-integration-extractor",
			Version: "v1",
		},
		Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "replacement",
			StatementText:   statement,
			EvidenceRefs:    []string{"span:S1"},
		}}},
	})
	if err != nil {
		t.Fatalf("SubmitExtractorOutput(%s) error = %v", requestSuffix, err)
	}
	return result
}

func integrationClosureLineageKey(t *testing.T, basis SupersessionLineageBasis) string {
	t.Helper()
	domainBasis := evidencesupersession.Basis{
		SourceSystem:    basis.SourceSystem,
		SourceNamespace: basis.SourceNamespace,
		ObjectType:      basis.ObjectType,
		ObjectID:        basis.ObjectID,
		SlotKind:        basis.SlotKind,
		SlotID:          basis.SlotID,
	}.Normalize()
	key, err := domainBasis.Key()
	if err != nil {
		t.Fatalf("identify supersession closure lineage: %v", err)
	}
	return key
}

func integrationClosureEdgeByTarget(
	t *testing.T,
	result SupersessionAdmissionResult,
) map[string]string {
	t.Helper()
	if len(result.TargetNodeIDs) != len(result.SupersedesEdgeIDs) {
		t.Fatalf("supersession targets/edges differ: %+v", result)
	}
	edges := make(map[string]string, len(result.TargetNodeIDs))
	for index, targetID := range result.TargetNodeIDs {
		edges[targetID] = result.SupersedesEdgeIDs[index]
	}
	return edges
}

func sortedIntegrationClosureStrings(values ...string) []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	return result
}
