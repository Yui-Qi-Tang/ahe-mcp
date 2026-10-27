//go:build integration

package evidenceingestion

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TEST APPROVAL STUB: corruption is confined to the disposable fixture schema.
// A derivation's proposal carrier need not be one of its registered AND leaves.
func TestIntegrationHistoricalAncestorDistinctSourceIntegrityAndReplay(t *testing.T) {
	for _, kind := range []string{"historical", "ordinary"} {
		for _, material := range []string{"raw", "rendered"} {
			t.Run(kind+"/"+material, func(t *testing.T) {
				ctx, pool := integrationPoolWithMigrations(t)
				sources := historicalSources(t, ctx, pool, 2, false)
				req := historicalProposal(t, ctx, pool, "distinct-carrier", sources[1])
				req.Derivation.ParentNodeIDs = []string{sources[0]}
				var ancestor string
				if kind == "historical" {
					h, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, req, "distinct-admit"))
					if err != nil {
						t.Fatal(err)
					}
					ancestor = h.Admission.CanonicalRef
				} else {
					d := req.Derivation
					h, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: req.ProposalOccurrenceID, Derivation: &DerivationAdmissionInput{ParentNodeIDs: d.ParentNodeIDs, Method: d.Method, Producer: d.Producer, TraceRef: d.TraceRef}})
					if err != nil {
						t.Fatal(err)
					}
					ancestor = h.CanonicalRef
				}
				wrapper := historicalProposal(t, ctx, pool, "saved-wrapper", sources[0])
				wrapper.Derivation.ParentNodeIDs = []string{ancestor}
				decision := historicalDecision(t, ctx, pool, wrapper, "saved-wrapper-admit")
				saved, err := ApplyHistoricalDerivationDecision(ctx, pool, decision)
				if err != nil {
					t.Fatal("uncorrupted ancestor control", err)
				}
				original, err := ReadHistoricalDerivationHistory(ctx, pool, wrapper.ProposalOccurrenceID)
				if err != nil {
					t.Fatal(err)
				}
				if len(original.Display.AncestorSources) != 1 || original.Display.AncestorSources[0].Node.CanonicalID != ancestor || original.Display.AncestorSources[0].Node.OriginProposal.SourceSnapshotID == original.Display.Basis.SourceLeaves[0].Node.OriginProposal.SourceSnapshotID {
					t.Fatal("distinct ancestor carrier was omitted or mislabeled as an AND leaf")
				}
				if original.Display.AncestorSources[0].RawText == "" || original.Display.AncestorSources[0].RenderedText == "" {
					t.Fatal("ancestor source bytes absent from review")
				}
				pending := []HistoricalDerivationDecision{}
				for _, parent := range []string{ancestor, saved.Admission.CanonicalRef} {
					child := historicalProposal(t, ctx, pool, "pending-"+parent, sources[0])
					child.Derivation.ParentNodeIDs = []string{parent}
					pending = append(pending, historicalDecision(t, ctx, pool, child, "admit-"+parent))
				}
				carrier, err := GetCanonicalEvidenceByID(ctx, pool, sources[1])
				if err != nil {
					t.Fatal(err)
				}
				// Commit the injected fixture corruption so both public read and
				// public writer transactions observe it. The schema is discarded
				// by integrationPool; no production or shared evidence is used.
				err = withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
					if material == "raw" {
						if _, err := tx.exec(ctx, `ALTER TABLE source_blobs DISABLE TRIGGER USER`); err != nil {
							return err
						}
						if _, err := tx.exec(ctx, `UPDATE source_blobs SET raw_content=convert_to('corrupt carrier bytes','UTF8') WHERE raw_content_hash=$1`, carrier.OriginProposal.RawContentHash); err != nil {
							return err
						}
						_, err := tx.exec(ctx, `ALTER TABLE source_blobs ENABLE TRIGGER USER`)
						return err
					}
					if _, err := tx.exec(ctx, `ALTER TABLE extraction_views DISABLE TRIGGER USER`); err != nil {
						return err
					}
					if _, err := tx.exec(ctx, `UPDATE extraction_views SET rendered_content=convert_to('corrupt rendered carrier','UTF8') WHERE extraction_view_id=$1`, carrier.OriginProposal.ExtractionViewID); err != nil {
						return err
					}
					_, err := tx.exec(ctx, `ALTER TABLE extraction_views ENABLE TRIGGER USER`)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				before := historicalCounts(t, ctx, pool)
				var revision int64
				if err := pool.QueryRow(ctx, `SELECT revision FROM evidence_governance_head WHERE singleton`).Scan(&revision); err != nil {
					t.Fatal(err)
				}
				for _, child := range pending {
					if _, err := LoadHistoricalDerivationReview(ctx, pool, child.Review); err == nil {
						t.Error("new historical review accepted corrupt ancestor source")
					}
					if _, err := ApplyHistoricalDerivationDecision(ctx, pool, child); err == nil {
						t.Error("stale approval admitted corrupt ancestor source")
					}
					p, err := GetProposalByOccurrenceID(ctx, pool, child.Review.ProposalOccurrenceID)
					if err != nil || p.AdmissionOutcome != admissionOutcomePending {
						t.Error("failed decision changed proposal", err)
					}
				}
				// Saved approval is historical evidence. Do not re-evaluate its
				// ancestors or rewrite its original subject/display on exact retry.
				replayed, err := ApplyHistoricalDerivationDecision(ctx, pool, decision)
				if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Admission, saved.Admission) {
					t.Fatal("saved receipt replay changed", err)
				}
				history, err := ReadHistoricalDerivationHistory(ctx, pool, wrapper.ProposalOccurrenceID)
				if err != nil || !reflect.DeepEqual(history, original) {
					t.Fatal("saved historical display changed", err)
				}
				var afterRevision int64
				if err := pool.QueryRow(ctx, `SELECT revision FROM evidence_governance_head WHERE singleton`).Scan(&afterRevision); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, historicalCounts(t, ctx, pool)) || revision != afterRevision {
					t.Fatal("rejected decisions or replay changed persistent authority")
				}
			})
		}
	}
}

func TestIntegrationHistoricalAncestorSourcesCountTowardReviewBound(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parent := historicalSources(t, ctx, pool, 1, false)[0]
	ancestors := []string{}
	for i := range 4 {
		label := fmt.Sprintf("large-ancestor-%d", i)
		envelope := validExternalSourceEnvelope(label)
		envelope.Content = "Carrier context.\n" + strings.Repeat(strings.Repeat("x", 1599)+"\n", 100)
		source, err := CaptureExternalSource(ctx, pool, envelope)
		if err != nil {
			t.Fatal(err)
		}
		proposal, err := submitHistoricalFixtureOutput(ctx, pool, ExtractorOutputInput{RequestID: label + "-extract", SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID, Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "J", StatementText: "Synthetic derived conclusion " + label, EvidenceRefs: []string{"span:S1"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		// Legacy ordinary fixture setup does not grant current-use clearance.
		a, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID, Derivation: &DerivationAdmissionInput{ParentNodeIDs: []string{parent}, Method: "synthetic bounded derivation", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:" + label}})
		if err != nil {
			t.Fatal(err)
		}
		ancestors = append(ancestors, a.CanonicalRef)
	}
	slices.Sort(ancestors)
	req := historicalProposal(t, ctx, pool, "large-ancestor-child", parent)
	req.Derivation.ParentNodeIDs = ancestors[:1]
	control, err := LoadHistoricalDerivationReview(ctx, pool, req)
	if err != nil || len(control.Display.AncestorSources) != 1 || len(control.Display.AncestorSources[0].RawText) < 160000 {
		t.Fatal("individual large ancestor source control", err)
	}
	req.Derivation.ParentNodeIDs = ancestors
	before := historicalCounts(t, ctx, pool)
	if _, err := LoadHistoricalDerivationReview(ctx, pool, req); err == nil || !strings.Contains(err.Error(), "review bound") {
		t.Fatal("cumulative ancestor source bytes escaped the historical display bound", err)
	}
	if _, err := ApplyHistoricalDerivationDecision(ctx, pool, HistoricalDerivationDecision{RequestID: "oversized-ancestor-admit", Review: req, ExpectedSubject: control.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}); err == nil {
		t.Fatal("oversized historical review admitted")
	}
	if !reflect.DeepEqual(before, historicalCounts(t, ctx, pool)) {
		t.Fatal("oversized review partially persisted")
	}
}
