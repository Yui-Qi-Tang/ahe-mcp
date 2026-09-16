//go:build integration

package evidenceingestion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type validationE2EProposalFactory func(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	objectID string,
	revision string,
	statement string,
) IngestResult

func TestIntegrationSupersessionValidationE2EProductionPath(t *testing.T) {
	ctx, pool := validationE2EPool(t)
	validationE2ERunProductionPath(t, ctx, pool, validationE2ECreateProposal)
}

func validationE2ERunProductionPath(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	createProposal validationE2EProposalFactory,
) {
	t.Helper()

	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead(initial) error = %v", err)
	}
	if head.ChainKey != evidencesupersession.ChainKey || head.Revision != 0 || head.HeadEventID != "" {
		t.Fatalf("initial supersession head = %+v, want canonical revision 0", head)
	}
	if got := validationE2EAuthorityCounts(t, ctx, pool); got != (validationE2ECounts{}) {
		t.Fatalf("fresh migrated authority counts = %+v, want zero", got)
	}

	mainBasis := validationE2EBasis("VAL-42")
	mainLineageKey := validationE2ELineageKey(t, mainBasis)
	v1Proposal := createProposal(t, ctx, pool, "main-v1", mainBasis.ObjectID, "r1", "Validation policy is v1.")
	v1, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: v1Proposal.ProposalOccurrenceID,
		DecisionBy:           "validation-reviewer",
		DecisionReason:       "admit the initial synthetic source claim",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(v1) error = %v", err)
	}
	validationE2EAssertTemporalUnknown(t, ctx, pool, v1.CanonicalRef)
	if got := validationE2EAuthorityCounts(t, ctx, pool); got != (validationE2ECounts{
		CanonicalNodes: 2, CanonicalEdges: 1, AdmissionDecisions: 1,
	}) {
		t.Fatalf("ordinary v1 authority counts = %+v", got)
	}
	head, err = GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead(after v1) error = %v", err)
	}
	if head.Revision != 0 || head.HeadEventID != "" {
		t.Fatalf("ordinary admission moved supersession head = %+v", head)
	}

	v2Proposal := createProposal(t, ctx, pool, "main-v2", mainBasis.ObjectID, "r2", "Validation policy is v2.")
	v2Input := SupersessionAdmissionInput{
		ProposalOccurrenceID: v2Proposal.ProposalOccurrenceID,
		DecisionBy:           "validation-reviewer",
		DecisionReason:       "v2 replaces the reviewed v1 claim",
		Basis:                mainBasis,
		TargetNodeIDs:        []string{v1.CanonicalRef},
	}
	v2, err := AdmitPendingSupersession(ctx, pool, v2Input)
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(v2) error = %v", err)
	}
	if v2.EventRevision != 1 || v2.PreviousHeadEventID != "" || v2.LineageKey != mainLineageKey ||
		!slices.Equal(v2.TargetNodeIDs, []string{v1.CanonicalRef}) ||
		!slices.Equal(v2.BootstrappedTargetNodeIDs, []string{v1.CanonicalRef}) ||
		len(v2.SupersedesEdgeIDs) != 1 {
		t.Fatalf("v2 supersession result = %+v", v2)
	}
	var eventRevision, previousRevision int64
	var previousEventID, eventLineage, replacementNodeID string
	if err := pool.QueryRow(ctx, `
		SELECT revision, previous_revision, COALESCE(previous_event_id, ''),
			lineage_key, replacement_node_id
		FROM canonical_supersession_admission_events
		WHERE event_id = $1
	`, v2.AdmissionEventID).Scan(
		&eventRevision,
		&previousRevision,
		&previousEventID,
		&eventLineage,
		&replacementNodeID,
	); err != nil {
		t.Fatalf("load v2 admission event: %v", err)
	}
	if eventRevision != 1 || previousRevision != 0 || previousEventID != "" ||
		eventLineage != mainLineageKey || replacementNodeID != v2.CanonicalRef {
		t.Fatalf("persisted v2 event = revision %d/%d previous %q lineage %s replacement %s",
			eventRevision, previousRevision, previousEventID, eventLineage, replacementNodeID)
	}
	var edgeFrom, edgeTo, edgeRelation string
	if err := pool.QueryRow(ctx, `
		SELECT from_node_id, to_node_id, relation
		FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
	`, v2.SupersedesEdgeIDs[0]).Scan(&edgeFrom, &edgeTo, &edgeRelation); err != nil {
		t.Fatalf("load v2 supersedes edge: %v", err)
	}
	if edgeFrom != v2.CanonicalRef || edgeTo != v1.CanonicalRef ||
		edgeRelation != string(evidencegraph.CanonicalSupersedes) {
		t.Fatalf("v2 supersedes edge = %s -> %s / %s", edgeFrom, edgeTo, edgeRelation)
	}
	if got := validationE2EAuthorityCounts(t, ctx, pool); got != (validationE2ECounts{
		CanonicalNodes: 4, CanonicalEdges: 3, AdmissionDecisions: 2,
		Lineages: 1, Events: 1, Members: 2, ReplacementTargets: 1,
	}) {
		t.Fatalf("v2 authority counts = %+v", got)
	}

	chain := validationE2EReadCurrentness(t, ctx, pool, mainLineageKey)
	validationE2EAssertCurrentness(t, chain, mainLineageKey, v2.EventRevision, v2.AdmissionEventID, true,
		map[string]evidencesupersession.CurrentnessStatus{
			v1.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v2.CanonicalRef: evidencesupersession.CurrentnessCurrent,
		},
		[]string{v2.CanonicalRef},
	)

	v3Proposal := createProposal(t, ctx, pool, "main-v3", mainBasis.ObjectID, "r3", "Validation branch policy is v3.")
	v3, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v3Proposal.ProposalOccurrenceID,
		DecisionBy:           "validation-reviewer",
		DecisionReason:       "v3 forms a reviewed branch from v1",
		Basis:                mainBasis,
		TargetNodeIDs:        []string{v1.CanonicalRef},
		ExpectedRevision:     v2.EventRevision,
		ExpectedHeadEventID:  v2.AdmissionEventID,
	})
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(v3 branch) error = %v", err)
	}
	branch := validationE2EReadCurrentness(t, ctx, pool, mainLineageKey)
	validationE2EAssertCurrentness(t, branch, mainLineageKey, v3.EventRevision, v3.AdmissionEventID, true,
		map[string]evidencesupersession.CurrentnessStatus{
			v1.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v2.CanonicalRef: evidencesupersession.CurrentnessAmbiguous,
			v3.CanonicalRef: evidencesupersession.CurrentnessAmbiguous,
		},
		validationE2ESorted(v2.CanonicalRef, v3.CanonicalRef),
	)
	if branch.Projection.CutHash == chain.Projection.CutHash ||
		branch.Projection.ObjectClaimManifestHash == chain.Projection.ObjectClaimManifestHash {
		t.Fatalf("branch did not advance cut/object manifest: chain=%+v branch=%+v", chain.Projection, branch.Projection)
	}

	v4Proposal := createProposal(t, ctx, pool, "main-v4", mainBasis.ObjectID, "r4", "Validation merged policy is v4.")
	v4, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v4Proposal.ProposalOccurrenceID,
		DecisionBy:           "validation-reviewer",
		DecisionReason:       "v4 merges both reviewed frontiers",
		Basis:                mainBasis,
		TargetNodeIDs:        []string{v2.CanonicalRef, v3.CanonicalRef},
		ExpectedRevision:     v3.EventRevision,
		ExpectedHeadEventID:  v3.AdmissionEventID,
	})
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(v4 merge) error = %v", err)
	}
	merged := validationE2EReadCurrentness(t, ctx, pool, mainLineageKey)
	validationE2EAssertCurrentness(t, merged, mainLineageKey, v4.EventRevision, v4.AdmissionEventID, true,
		map[string]evidencesupersession.CurrentnessStatus{
			v1.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v2.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v3.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v4.CanonicalRef: evidencesupersession.CurrentnessCurrent,
		},
		[]string{v4.CanonicalRef},
	)

	otherBasis := validationE2EBasis("VAL-OTHER")
	otherV1Proposal := createProposal(t, ctx, pool, "other-v1", otherBasis.ObjectID, "r1", "Other validation policy is v1.")
	otherV1, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: otherV1Proposal.ProposalOccurrenceID,
		DecisionBy:           "validation-reviewer",
		DecisionReason:       "admit unrelated synthetic v1",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(other v1) error = %v", err)
	}
	otherV2Proposal := createProposal(t, ctx, pool, "other-v2", otherBasis.ObjectID, "r2", "Other validation policy is v2.")
	otherV2, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: otherV2Proposal.ProposalOccurrenceID,
		DecisionBy:           "validation-reviewer",
		DecisionReason:       "unrelated lineage advances the global head",
		Basis:                otherBasis,
		TargetNodeIDs:        []string{otherV1.CanonicalRef},
		ExpectedRevision:     v4.EventRevision,
		ExpectedHeadEventID:  v4.AdmissionEventID,
	})
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(other v2) error = %v", err)
	}
	afterGlobal := validationE2EReadCurrentness(t, ctx, pool, mainLineageKey)
	validationE2EAssertCurrentness(t, afterGlobal, mainLineageKey, otherV2.EventRevision, otherV2.AdmissionEventID, true,
		map[string]evidencesupersession.CurrentnessStatus{
			v1.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v2.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v3.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v4.CanonicalRef: evidencesupersession.CurrentnessCurrent,
		},
		[]string{v4.CanonicalRef},
	)
	if afterGlobal.Projection.Head == merged.Projection.Head ||
		afterGlobal.Projection.HistoryHash == merged.Projection.HistoryHash ||
		afterGlobal.Projection.CutHash == merged.Projection.CutHash ||
		afterGlobal.Projection.Witness.ID == merged.Projection.Witness.ID {
		t.Fatalf("unrelated event did not advance exact global coordinate: merged=%+v after=%+v", merged.Projection, afterGlobal.Projection)
	}
	if afterGlobal.Projection.ObjectClaimManifestHash != merged.Projection.ObjectClaimManifestHash ||
		!reflect.DeepEqual(afterGlobal.Projection.Nodes, merged.Projection.Nodes) ||
		!slices.Equal(afterGlobal.Projection.FrontierNodeIDs, merged.Projection.FrontierNodeIDs) {
		t.Fatalf("unrelated event changed selected object/topology: merged=%+v after=%+v", merged.Projection, afterGlobal.Projection)
	}

	unclassifiedProposal := createProposal(
		t, ctx, pool, "main-unclassified", mainBasis.ObjectID, "r5", "A synthetic same-object claim has no reviewed slot.",
	)
	unclassified, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: unclassifiedProposal.ProposalOccurrenceID,
		DecisionBy:           "validation-reviewer",
		DecisionReason:       "ordinary admission without lineage classification",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(unclassified) error = %v", err)
	}
	partial := validationE2EReadCurrentness(t, ctx, pool, mainLineageKey)
	validationE2EAssertCurrentness(t, partial, mainLineageKey, otherV2.EventRevision, otherV2.AdmissionEventID, false,
		map[string]evidencesupersession.CurrentnessStatus{
			v1.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v2.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v3.CanonicalRef: evidencesupersession.CurrentnessSuperseded,
			v4.CanonicalRef: evidencesupersession.CurrentnessUnknown,
		},
		[]string{v4.CanonicalRef},
	)
	if partial.Projection.Head != afterGlobal.Projection.Head ||
		partial.Projection.HistoryHash != afterGlobal.Projection.HistoryHash ||
		partial.Projection.CutHash != afterGlobal.Projection.CutHash ||
		partial.Projection.ObjectClaimManifestHash == afterGlobal.Projection.ObjectClaimManifestHash {
		t.Fatalf("unclassified claim changed the wrong coordinate: complete=%+v partial=%+v", afterGlobal.Projection, partial.Projection)
	}

	beforeReplayCounts := validationE2EAuthorityCounts(t, ctx, pool)
	beforeReplayHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead(before replay) error = %v", err)
	}
	replay, err := AdmitPendingSupersession(ctx, pool, v2Input)
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(late v2 replay) error = %v", err)
	}
	if !replay.Replayed || replay.AdmissionEventID != v2.AdmissionEventID ||
		replay.CanonicalRef != v2.CanonicalRef || replay.EventRevision != v2.EventRevision {
		t.Fatalf("late v2 replay = %+v, want original %+v", replay, v2)
	}
	afterReplayCounts := validationE2EAuthorityCounts(t, ctx, pool)
	afterReplayHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead(after replay) error = %v", err)
	}
	if afterReplayCounts != beforeReplayCounts || afterReplayHead != beforeReplayHead {
		t.Fatalf("late replay mutated authority: counts %+v -> %+v, head %+v -> %+v",
			beforeReplayCounts, afterReplayCounts, beforeReplayHead, afterReplayHead)
	}
	afterReplay := validationE2EReadCurrentness(t, ctx, pool, mainLineageKey)
	if !reflect.DeepEqual(afterReplay, partial) {
		t.Fatalf("late replay changed currentness: before=%+v after=%+v", partial, afterReplay)
	}

	if got := validationE2EAuthorityCounts(t, ctx, pool); got != (validationE2ECounts{
		CanonicalNodes: 14, CanonicalEdges: 12, AdmissionDecisions: 7,
		Lineages: 2, Events: 4, Members: 6, ReplacementTargets: 5,
	}) {
		t.Fatalf("final authority counts = %+v", got)
	}
	for _, nodeID := range []string{
		v1.CanonicalRef,
		v2.CanonicalRef,
		v3.CanonicalRef,
		v4.CanonicalRef,
		otherV1.CanonicalRef,
		otherV2.CanonicalRef,
		unclassified.CanonicalRef,
	} {
		validationE2EAssertTemporalUnknown(t, ctx, pool, nodeID)
	}
}

type validationE2ECounts struct {
	CanonicalNodes     int
	CanonicalEdges     int
	AdmissionDecisions int
	Lineages           int
	Events             int
	Members            int
	ReplacementTargets int
}

func validationE2EPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	return validationE2EPoolWithTimeout(t, 60*time.Second)
}

func validationE2EPoolWithTimeout(t *testing.T, timeout time.Duration) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect validation E2E admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	schema := "ahe_supersession_validation_" + validationE2ERandomHex(t, 8)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+quotedSchema); err != nil {
		t.Fatalf("create validation E2E schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+quotedSchema+` CASCADE`)
	})

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse validation E2E database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open validation E2E pool: %v", err)
	}
	t.Cleanup(pool.Close)

	changed, err := migrations.ApplyUp(ctx, pool)
	if err != nil {
		t.Fatalf("migrations.ApplyUp() error = %v", err)
	}
	if !changed {
		t.Fatal("migrations.ApplyUp() changed = false on a fresh isolated schema")
	}
	status, err := migrations.VerifyCurrent(ctx, pool)
	if err != nil {
		t.Fatalf("migrations.VerifyCurrent() error = %v", err)
	}
	if status.AppliedMigrations != 49 || status.LatestMigration != "000049_evidence_ingestion_endpoint_review.up.sql" {
		t.Fatalf("verified migration status = %+v", status)
	}
	return ctx, pool
}

func validationE2ECreateProposal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	objectID string,
	revision string,
	statement string,
) IngestResult {
	t.Helper()
	source := validationE2ECaptureSource(t, ctx, pool, suffix, objectID, revision, statement)
	result, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID:        "validation-e2e-extractor-" + suffix,
		SourceSnapshotID: source.SourceSnapshotID,
		ExtractionViewID: source.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{
			Name:    "validation-e2e-extractor",
			Version: "v1",
		},
		Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "claim",
			StatementText:   statement,
			EvidenceRefs:    []string{"span:S1"},
		}}},
	})
	if err != nil {
		t.Fatalf("SubmitExtractorOutput(%s) error = %v", suffix, err)
	}
	return result
}

func validationE2ECaptureSource(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	objectID string,
	revision string,
	statement string,
) ExternalSourceIntakeResult {
	t.Helper()
	envelope := ExternalSourceEnvelopeV1{
		SchemaVersion:   ExternalSourceEnvelopeSchemaV1,
		RequestID:       "validation-e2e-source-" + suffix,
		SourceSystem:    "jira",
		SourceNamespace: "validation/acme",
		ObjectType:      "issue",
		ObjectID:        objectID,
		Revision:        revision,
		SourceLocation:  fmt.Sprintf("https://validation.invalid/jira/%s", objectID),
		Title:           "Synthetic supersession validation " + objectID,
		ContentFormat:   ExternalSourceContentFormatMarkdown,
		ContentFidelity: ExternalSourceContentFidelityVerbatim,
		Content:         statement + "\n",
		Coverage:        ExternalSourceCoverageFullDocument,
		CollectorID:     "validation-e2e-collector",
		ConnectorID:     "validation-e2e-connector",
		ObservedAt:      "2026-08-28T01:02:03Z",
		SourceCreatedAt: "2026-08-28T01:00:00Z",
	}
	source, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil {
		t.Fatalf("CaptureExternalSource(%s) error = %v", suffix, err)
	}
	return source
}

func validationE2EBasis(objectID string) SupersessionLineageBasis {
	return SupersessionLineageBasis{
		SourceSystem:    "jira",
		SourceNamespace: "validation/acme",
		ObjectType:      "issue",
		ObjectID:        objectID,
		SlotKind:        "field",
		SlotID:          "description",
	}
}

func validationE2ELineageKey(t *testing.T, basis SupersessionLineageBasis) string {
	t.Helper()
	key, err := (evidencesupersession.Basis{
		SourceSystem:    basis.SourceSystem,
		SourceNamespace: basis.SourceNamespace,
		ObjectType:      basis.ObjectType,
		ObjectID:        basis.ObjectID,
		SlotKind:        basis.SlotKind,
		SlotID:          basis.SlotID,
	}).Key()
	if err != nil {
		t.Fatalf("identify validation E2E lineage: %v", err)
	}
	return key
}

func validationE2EReadCurrentness(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	lineageKey string,
) CanonicalSupersessionCurrentness {
	t.Helper()
	beforeCounts := validationE2EAuthorityCounts(t, ctx, pool)
	beforeHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head before validation E2E currentness read: %v", err)
	}
	result, err := GetCanonicalSupersessionCurrentness(ctx, pool, lineageKey)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionCurrentness() error = %v", err)
	}
	afterCounts := validationE2EAuthorityCounts(t, ctx, pool)
	afterHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head after validation E2E currentness read: %v", err)
	}
	if afterCounts != beforeCounts || afterHead != beforeHead {
		t.Fatalf("currentness read mutated authority: counts %+v -> %+v, head %+v -> %+v",
			beforeCounts, afterCounts, beforeHead, afterHead)
	}
	return result
}

func validationE2EAssertCurrentness(
	t *testing.T,
	result CanonicalSupersessionCurrentness,
	lineageKey string,
	wantRevision int64,
	wantHeadEventID string,
	wantClosure bool,
	wantStatuses map[string]evidencesupersession.CurrentnessStatus,
	wantFrontiers []string,
) {
	t.Helper()
	projection := result.Projection
	if projection.ContractVersion != evidencesupersession.ClosureCutContractVersionV1 ||
		projection.AlgorithmVersion != evidencesupersession.CurrentnessAlgorithmVersionV1 ||
		projection.Semantics != evidencesupersession.CurrentnessSemanticsV1 ||
		projection.CoveragePolicy != evidencesupersession.ObjectCoveragePolicyV1 ||
		projection.LineageKey != lineageKey ||
		projection.Head.ChainKey != evidencesupersession.ChainKey ||
		projection.Head.Revision != wantRevision ||
		projection.Head.HeadEventID != wantHeadEventID {
		t.Fatalf("currentness contract/head = %+v", projection)
	}
	if projection.HistoryHash == "" || projection.CutHash == "" || projection.ObjectClaimManifestHash == "" {
		t.Fatalf("currentness has empty binding hash: %+v", projection)
	}
	if projection.ClosureAvailable != wantClosure {
		t.Fatalf("ClosureAvailable = %t, want %t", projection.ClosureAvailable, wantClosure)
	}
	if !slices.Contains(result.Limitations, canonicalSupersessionSnapshotLimitation) {
		t.Fatalf("currentness limitations = %v, want snapshot boundary", result.Limitations)
	}
	if wantClosure {
		if projection.Witness == nil || projection.Witness.ID == "" ||
			projection.Witness.Head != projection.Head ||
			projection.Witness.HistoryHash != projection.HistoryHash ||
			projection.Witness.CutHash != projection.CutHash ||
			projection.Witness.ObjectClaimManifestHash != projection.ObjectClaimManifestHash {
			t.Fatalf("closure witness does not bind projection: %+v", projection)
		}
	} else {
		if projection.Witness != nil ||
			!slices.Contains(result.Limitations, canonicalSupersessionUnclassifiedObjectClaimsLimitation) {
			t.Fatalf("partial currentness witness/limitations = %+v / %v", projection.Witness, result.Limitations)
		}
	}

	gotFrontiers := append([]string(nil), projection.FrontierNodeIDs...)
	slices.Sort(gotFrontiers)
	wantFrontiers = append([]string(nil), wantFrontiers...)
	slices.Sort(wantFrontiers)
	if !slices.Equal(gotFrontiers, wantFrontiers) {
		t.Fatalf("currentness frontiers = %v, want %v", gotFrontiers, wantFrontiers)
	}
	if len(projection.Nodes) != len(wantStatuses) {
		t.Fatalf("currentness nodes = %+v, want %d", projection.Nodes, len(wantStatuses))
	}
	for _, node := range projection.Nodes {
		want, exists := wantStatuses[node.NodeID]
		if !exists || node.Status != want {
			t.Fatalf("currentness node %s status = %s, want %s (exists=%t)", node.NodeID, node.Status, want, exists)
		}
	}
}

func validationE2EAuthorityCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) validationE2ECounts {
	t.Helper()
	var result validationE2ECounts
	queries := []struct {
		table string
		out   *int
	}{
		{table: "canonical_graph_nodes", out: &result.CanonicalNodes},
		{table: "canonical_graph_edges", out: &result.CanonicalEdges},
		{table: "admission_decisions", out: &result.AdmissionDecisions},
		{table: "canonical_supersession_lineages", out: &result.Lineages},
		{table: "canonical_supersession_admission_events", out: &result.Events},
		{table: "canonical_supersession_members", out: &result.Members},
		{table: "canonical_supersession_replacement_targets", out: &result.ReplacementTargets},
	}
	for _, query := range queries {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+query.table).Scan(query.out); err != nil {
			t.Fatalf("count %s: %v", query.table, err)
		}
	}
	return result
}

func validationE2EAssertTemporalUnknown(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nodeID string) {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT temporal ->> 'status'
		FROM canonical_graph_nodes
		WHERE canonical_node_id = $1
	`, nodeID).Scan(&status); err != nil {
		t.Fatalf("load temporal status for %s: %v", nodeID, err)
	}
	if status != string(evidencegraph.TemporalUnknown) {
		t.Fatalf("temporal status for %s = %q, want %q", nodeID, status, evidencegraph.TemporalUnknown)
	}
}

func validationE2ESorted(values ...string) []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	return result
}

func validationE2ERandomHex(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate validation E2E schema suffix: %v", err)
	}
	return hex.EncodeToString(buf)
}
