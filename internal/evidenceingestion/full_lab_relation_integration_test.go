//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fullLabRelationMaterial struct {
	Proposal  CanonicalContradictionProposal
	Endpoints []CanonicalQueryResult
	Decision  *CanonicalContradictionDecision
	Sources   []BoundedSourceViewResult
}

func fullLabReadRelation(ctx context.Context, tx sqlTx, id string) (fullLabRelationMaterial, error) {
	p, d, err := loadCanonicalContradictionProposalByID(ctx, tx, id, false)
	m := fullLabRelationMaterial{Proposal: p, Decision: d}
	if err != nil {
		return m, err
	}
	for _, id := range []string{p.NodeAID, p.NodeBID} {
		n, e := getCanonicalEvidenceByID(ctx, tx, id)
		if e != nil {
			return m, e
		}
		m.Endpoints = append(m.Endpoints, n)
		v, e := loadBoundedSourceViewFromQueryer(ctx, tx, n.OriginProposal.SourceSnapshotID, n.OriginProposal.ExtractionViewID)
		if e != nil {
			return m, e
		}
		if v.Input == nil {
			return m, errors.New("source unavailable")
		}
		for _, r := range n.OriginProposal.SourceRefs {
			text := v.Input.RenderedText
			if r.StartByte < 0 || r.EndByte < r.StartByte || r.EndByte > len(text) || text[r.StartByte:r.EndByte] != r.QuotedText {
				return m, errors.New("quote/source mismatch")
			}
		}
		m.Sources = append(m.Sources, v)
	}
	return m, fullLabValidateProductRelation(m, 128<<10)
}

// Supplemental native contract: product provides two endpoint sources, not the Lab third relation-source claim.
func fullLabValidateProductRelation(m fullLabRelationMaterial, limit int) error {
	if len(m.Endpoints) != 2 || len(m.Sources) != 2 {
		return errors.New("incomplete endpoints")
	}
	for i, id := range []string{m.Proposal.NodeAID, m.Proposal.NodeBID} {
		n := m.Endpoints[i]
		if n.CanonicalID != id || len(n.OriginProposal.SourceRefs) == 0 || n.OriginProposal.SourceSnapshotID == "" {
			return errors.New("missing endpoint source")
		}
	}
	if m.Proposal.AdmissionOutcome == admissionOutcomePending && m.Decision != nil {
		return errors.New("mixed snapshot")
	}
	if m.Proposal.AdmissionOutcome != admissionOutcomePending && (m.Decision == nil || m.Decision.Outcome != m.Proposal.AdmissionOutcome || m.Decision.ProposalID != m.Proposal.ID) {
		return errors.New("mixed decision")
	}
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if len(b) > limit {
		return errors.New("material byte limit exceeded")
	}
	return nil
}
func TestFullLabOriginalRelationContract(t *testing.T) {
	ctx, pool, _ := fullLabReplayPool(t)
	makeClaim := func(id, text string) AdmissionResult {
		in := ManualTextInput{SourceID: id, SourceVersion: "v1", Raw: []byte(text + "\n"), RequestID: id, AttemptNumber: 1}
		r, e := IngestManualText(ctx, pool, in, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: text, EvidenceRefs: []string{"span:S1"}}}})
		if e != nil {
			t.Fatal(e)
		}
		a, e := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: r.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic fixture source"})
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	a := makeClaim("research-relation-a", "The refund window is seven days.")
	b := makeClaim("research-relation-b", "The refund window is not seven days.")
	reports := []map[string]any{}
	for _, isolation := range []string{"REPEATABLE READ", "READ COMMITTED"} {
		t.Run(isolation, func(t *testing.T) {
			f := fullLabContradictionProposal(t, ctx, pool, "research-"+isolation, a.CanonicalRef, b.CanonicalRef, "The supplied statements disagree.")
			tx, e := (pgxDB{pool: pool}).begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.rollback(ctx)
			if _, e = tx.exec(ctx, "SET TRANSACTION ISOLATION LEVEL "+isolation+", READ ONLY"); e != nil {
				t.Fatal(e)
			}
			first, e := fullLabReadRelation(ctx, tx, f.Proposal.ID)
			if e != nil {
				t.Fatal(e)
			}
			row := map[string]any{"isolation": isolation, "before": first, "original_three_source_contract": "PRODUCT_CAPABILITY_MISSING"}
			reports = append(reports, row)
			t.Error("original relation claim requires its own source snapshot and quotes; native relation proposal has only two grounded endpoint sources")
			if first.Proposal.AdmissionOutcome != admissionOutcomePending {
				row["snapshot_sequence"] = "initial relation was not pending"
				t.Error("initial material wrong: original same-pair second request reused terminal proposal")
				return
			}
			_, e = RecordPendingCanonicalContradictionDisposition(ctx, pool, CanonicalContradictionDispositionInput{ProposalID: f.Proposal.ID, Outcome: ProposalDispositionAuditOnly, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic concurrent disposition"})
			if e != nil {
				t.Fatal(e)
			}
			second, e := fullLabReadRelation(ctx, tx, f.Proposal.ID)
			if e != nil {
				t.Fatal(e)
			}
			row["after_same_transaction"] = second
			expected := admissionOutcomePending
			if isolation == "READ COMMITTED" {
				expected = admissionOutcomeAuditOnly
			}
			if second.Proposal.AdmissionOutcome != expected {
				t.Error("unexpected snapshot")
			}
			if e = tx.commit(ctx); e != nil {
				t.Fatal(e)
			}
			var current fullLabRelationMaterial
			e = withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error { var e error; current, e = fullLabReadRelation(ctx, tx, f.Proposal.ID); return e })
			if e != nil {
				t.Fatal(e)
			}
			row["new_request"] = current
			if current.Proposal.AdmissionOutcome != admissionOutcomeAuditOnly || current.Decision == nil {
				t.Fatal("new request lacks new decision")
			}
			mixed := first
			mixed.Decision = current.Decision
			if fullLabValidateProductRelation(mixed, 128<<10) == nil {
				t.Error("accepted mixed material")
			}
			absent := current
			absent.Endpoints = nil
			if fullLabValidateProductRelation(absent, 128<<10) == nil {
				t.Error("accepted missing endpoint")
			}
			noSource := current
			noSource.Endpoints = append([]CanonicalQueryResult(nil), current.Endpoints...)
			noSource.Endpoints[0].OriginProposal.SourceRefs = nil
			if fullLabValidateProductRelation(noSource, 128<<10) == nil {
				t.Error("accepted missing source")
			}
			if fullLabValidateProductRelation(current, 1) == nil {
				t.Error("accepted byte overflow")
			}
			_, e = GetCanonicalContradictionProposal(ctx, pool, "occ:research-absent")
			if e == nil {
				t.Error("accepted absent proposal")
			}
			row["supplemental_native_contract_controls_completed"] = 5
		})
	}
	researchOutput(t, "relation.json", map[string]any{"reports": reports, "original_contract_passed": false, "scope": "original relation data and isolation sequence attempted; missing third source is retained as failure; two-source assertions are supplemental, not original PASS"})
}
