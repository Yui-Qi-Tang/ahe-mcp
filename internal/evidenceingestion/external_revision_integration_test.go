//go:build integration

package evidenceingestion

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

// TEST APPROVAL STUB: synthetic API response and capture transcript in a
// disposable database. This exercises native bindings, not provider truth.
func TestIntegrationCollectorAssignedRevisionGroundedPlan(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	envelope := validExternalSourceEnvelope("collector-response")
	envelope.SourceSystem, envelope.SourceNamespace, envelope.ObjectType = "synthetic-api", "https://example.test", "response"
	envelope.ObjectID, envelope.SourceLocation = "https://example.test/api/rule", "https://example.test/api/rule"
	envelope.SourceCreatedAt, envelope.SourceUpdatedAt = "", ""
	envelope.ContentFormat = ExternalSourceContentFormatJSON
	envelope.Content = `{"rule":"Synthetic API limit is 10.","request_stamp":"first"}`
	envelope.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(envelope.Content)))
	source, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := loadGovernanceSource(ctx, pgxDB{pool: pool}, source.SourceSnapshotID)
	if err != nil || string(retained.RetainedBytes) != envelope.Content {
		t.Fatal("response bytes changed", err)
	}
	for _, key := range []string{externalSourceOriginCreatedAtKey, externalSourceOriginUpdatedAtKey} {
		if _, exists := retained.Snapshot.OriginMetadata[key]; exists {
			t.Fatal("source timestamp invented", key)
		}
	}
	proposal, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID: "collector-extraction", SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{Name: "synthetic-collector-extractor", Version: "v1"},
		Output:              FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "rule", StatementText: "Synthetic API limit is 10.", EvidenceRefs: []string{"span:S1"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	approval := reviewedSourceClaimIntegrationInput(t, ctx, pool, proposal.ExtractionAttemptID, proposal.ProposalOccurrenceID)
	approval.DecisionBy, approval.DecisionReason = "TEST APPROVAL STUB", "TEST APPROVAL STUB: synthetic source claim"
	admitted, err := AdmitReviewedSourceClaim(ctx, pool, approval)
	if err != nil {
		t.Fatal(err)
	}
	envelope.RequestID, envelope.ObservedAt = "collector-observed-again", "2026-10-24T01:00:00Z"
	observed, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil || observed.SourceSnapshotID != source.SourceSnapshotID {
		t.Fatal("unchanged observation lost snapshot", err)
	}
	registration, err := LoadInitialLineageRegistrationReview(ctx, pool, InitialLineageRegistrationRequest{
		ClaimID: admitted.CanonicalRef, ObservationReceiptID: envelope.RequestID,
		Basis: evidencesupersession.Basis{SourceSystem: envelope.SourceSystem, SourceNamespace: envelope.SourceNamespace,
			ObjectType: envelope.ObjectType, ObjectID: envelope.ObjectID, SlotKind: "rule", SlotID: "limit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyInitialLineageRegistration(ctx, pool, InitialLineageRegistrationDecision{
		RequestID: "collector-register", Review: registration.Request, ExpectedSubject: registration.Subject,
		Decision: "approved", ReviewerID: "TEST APPROVAL STUB", Reason: "TEST APPROVAL STUB: independently reviewed synthetic observation",
	}); err != nil {
		t.Fatal(err)
	}
	actions := []evidenceuse.ActionBasis{{ActionID: "synthetic-action", BasisIDs: []string{admitted.CanonicalRef}}}
	before, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.GroundedActionV1, actions)
	if err != nil || before.Accepted {
		t.Fatal("digest substituted for missing source findings", err)
	}
	// The synthetic collector's transcript is a distinct retained material,
	// indexed by the response digest. No network call or real source is claimed.
	transcript, err := json.Marshal(map[string]string{"url": envelope.SourceLocation, "observed_at": envelope.ObservedAt, "response_body": envelope.Content})
	if err != nil {
		t.Fatal(err)
	}
	materialEnvelope := envelope
	materialEnvelope.RequestID, materialEnvelope.SourceSystem = "collector-transcript", "synthetic-capture-log"
	materialEnvelope.ObjectType, materialEnvelope.ObjectID = "capture", "capture-1"
	materialEnvelope.SourceLocation, materialEnvelope.Content = "https://example.test/captures/1", string(transcript)
	material, err := CaptureExternalSource(ctx, pool, materialEnvelope)
	if err != nil || material.SourceSnapshotID == source.SourceSnapshotID {
		t.Fatal("comparison transcript missing", err)
	}
	request := GovernanceRequest{Kind: "source_assessment", TargetID: source.SourceSnapshotID, Operation: "assess",
		MaterialIDs: []string{source.SourceSnapshotID, material.SourceSnapshotID}, Method: "TEST APPROVAL STUB: compare response, URL and coverage against synthetic capture transcript",
		CheckerName: "TEST APPROVAL STUB", CheckerVersion: "v1", CheckedAt: "2026-10-24T01:00:00Z",
		Limitations: []string{"Synthetic complete response and collector transcript; no real provider or authenticity claim."}}
	for _, criterion := range []string{evidenceuse.RetentionV1, evidenceuse.CorrespondenceV1, evidenceuse.CoverageV1} {
		ref := material.SourceSnapshotID
		if criterion == evidenceuse.RetentionV1 {
			ref = source.SourceSnapshotID
		}
		request.Checks = append(request.Checks, evidenceuse.SourceCheck{SourceSnapshotID: source.SourceSnapshotID, ContentHash: retained.Snapshot.RawContentHash,
			Criterion: criterion, Scope: evidenceuse.RetainedSnapshotScope, Result: evidenceuse.CheckPass, MaterialRef: ref, ComparedRevision: envelope.Revision})
	}
	wrong := request
	wrong.Checks = append([]evidenceuse.SourceCheck(nil), request.Checks...)
	wrong.Checks[1].ComparedRevision = "unrelated-capture"
	if _, err := LoadGovernanceReview(ctx, pool, wrong); err == nil {
		t.Fatal("finding for another capture accepted")
	}
	review, err := LoadGovernanceReview(ctx, pool, request)
	if err != nil || review.Display.Source == nil || len(review.Display.Materials) != 2 {
		t.Fatal("complete native source review unavailable", err)
	}
	if !reflect.DeepEqual(review.Display.Materials[1].RetainedBytes, transcript) {
		t.Fatal("comparison transcript not shown verbatim")
	}
	decision := GovernanceDecision{RequestID: "collector-source-assessment", Review: review.Display.Request, ExpectedSubject: review.Subject,
		Decision: "approved", ReviewerID: "TEST APPROVAL STUB", Reason: "TEST APPROVAL STUB: reviewed source fidelity and coverage with transcript"}
	first, err := ApplyGovernanceDecision(ctx, pool, decision)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ApplyGovernanceDecision(ctx, pool, decision)
	if err != nil || !replayed.Replayed || replayed.EventID != first.EventID {
		t.Fatal("source assessment did not replay", err)
	}
	after, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.GroundedActionV1, actions)
	if err != nil || !after.Accepted {
		t.Fatal("reviewed collector revision not usable under grounded policy", after.Assessment, err)
	}
	envelope.RequestID = "collector-response-changed"
	envelope.Content = `{"rule":"Synthetic API limit is 10.","request_stamp":"second"}`
	envelope.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(envelope.Content)))
	changed, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil || changed.SourceSnapshotID == source.SourceSnapshotID {
		t.Fatal("changed response did not retain a separate snapshot", err)
	}
	var edges int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_graph_edges WHERE relation='supersedes'`).Scan(&edges); err != nil || edges != 0 {
		t.Fatal("source capture inferred supersession", edges, err)
	}
	final, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.GroundedActionV1, actions)
	if err != nil || !final.Accepted {
		t.Fatal("unadmitted snapshot silently changed reviewed currentness", err)
	}
}
