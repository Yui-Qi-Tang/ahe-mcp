//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fullLabRelationMaterial struct {
	Proposal       CanonicalContradictionProposal
	Endpoints      []CanonicalQueryResult
	Decision       *CanonicalContradictionDecision
	Sources        []BoundedSourceViewResult
	SourceProposal *ProposalQueryResult
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
	if p.SourceProposalOccurrenceID != "" {
		source, err := loadContradictionSource(ctx, tx, p)
		if err != nil {
			return m, err
		}
		view, err := loadBoundedSourceViewFromQueryer(ctx, tx, source.SourceSnapshotID, source.ExtractionViewID)
		if err != nil {
			return m, err
		}
		if view.Input == nil {
			return m, errors.New("relation source unavailable")
		}
		for _, ref := range source.SourceRefs {
			text := view.Input.RenderedText
			if ref.StartByte < 0 || ref.EndByte < ref.StartByte || ref.EndByte > len(text) || text[ref.StartByte:ref.EndByte] != ref.QuotedText {
				return m, errors.New("relation quote/source mismatch")
			}
		}
		m.SourceProposal = &source
		m.Sources = append(m.Sources, view)
	}
	return m, fullLabValidateProductRelation(m, 128<<10)
}

// Source-bound relations include their exact third source in the same transaction.
// Legacy relations retain the two endpoint sources and do not invent a third.
func fullLabValidateProductRelation(m fullLabRelationMaterial, limit int) error {
	sources := 2
	if m.Proposal.SourceProposalOccurrenceID != "" {
		sources = 3
		if m.SourceProposal == nil || m.SourceProposal.ProposalOccurrenceID != m.Proposal.SourceProposalOccurrenceID || m.SourceProposal.StatementText != m.Proposal.Rationale || len(m.SourceProposal.SourceRefs) == 0 {
			return errors.New("missing or mixed relation source")
		}
	}
	if len(m.Endpoints) != 2 || len(m.Sources) != sources {
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
	reports := []map[string]any{}
	for _, isolation := range []string{"REPEATABLE READ", "READ COMMITTED"} {
		t.Run(isolation, func(t *testing.T) {
			// Each isolation probe starts pending. A terminal pair is intentionally single-use.
			a := makeClaim("research-relation-a-"+isolation, "The refund window is seven days.")
			b := makeClaim("research-relation-b-"+isolation, "The refund window is not seven days.")
			rationale := "The supplied statements disagree."
			source, e := IngestManualText(ctx, pool, ManualTextInput{SourceID: "relation-source-" + isolation, SourceVersion: "v1", Raw: []byte(rationale + "\n"), RequestID: "relation-source-" + isolation, AttemptNumber: 1}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "relation", StatementText: rationale, EvidenceRefs: []string{"span:S1"}}}})
			if e != nil {
				t.Fatal(e)
			}
			f, e := SubmitCanonicalContradictionProposal(ctx, pool, CanonicalContradictionProposalInput{RequestID: "research-" + isolation, NodeAID: a.CanonicalRef, NodeBID: b.CanonicalRef, Rationale: rationale, ProducerName: "frozen-lab-fixture", ProducerVersion: "v1", SourceProposalOccurrenceID: source.ProposalOccurrenceID})
			if e != nil {
				t.Fatal(e)
			}
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
			row := map[string]any{"isolation": isolation, "before": first, "original_three_source_contract": "source_bound_native"}
			reports = append(reports, row)
			if len(first.Sources) != 3 || first.SourceProposal == nil {
				t.Fatal("original three-source contract incomplete")
			}
			native, e := GetCanonicalContradictionProposal(ctx, pool, f.Proposal.ID)
			if e != nil || native.SourceProposal == nil || native.SourceProposal.ProposalOccurrenceID != source.ProposalOccurrenceID {
				t.Fatalf("native source readback: %+v %v", native, e)
			}
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
			noRelationSource := current
			noRelationSource.SourceProposal = nil
			if fullLabValidateProductRelation(noRelationSource, 128<<10) == nil {
				t.Error("accepted missing relation source")
			}
			row["native_contract_controls_completed"] = 6
		})
	}
	researchOutput(t, "relation.json", map[string]any{"reports": reports, "original_contract_passed": !t.Failed(), "scope": "native source-bound relation plus endpoint sources, same-snapshot reads, independent isolation fixtures; TEST APPROVAL STUB"})
}
