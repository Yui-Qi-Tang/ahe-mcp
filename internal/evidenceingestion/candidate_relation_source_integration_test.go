//go:build integration

package evidenceingestion

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationCandidateAdmissionPreservesBoundary(t *testing.T) {
	ctx, pool := integrationPool(t)
	parent := propositionTestClaim(t, ctx, pool, "candidate-parent")
	pending := func(name string) string {
		t.Helper()
		text := "Synthetic candidate " + name + "."
		got, err := IngestManualText(ctx, pool, ManualTextInput{SourceID: name, SourceVersion: "v1", Raw: []byte(text + "\n"), RequestID: name, AttemptNumber: 1}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: text, EvidenceRefs: []string{"span:S1"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return got.ProposalOccurrenceID
	}
	request := AdmissionInput{ProposalOccurrenceID: pending("candidate"), DecisionBy: "TEST APPROVAL STUB", DecisionReason: "record a hypothesis, not a fact", Candidate: &CandidateAdmissionInput{ParentNodeIDs: []string{parent}, Method: "declared-hypothesis", Producer: "fixture", TraceRef: "trace:candidate"}}
	admitted, err := AdmitPendingProposal(ctx, pool, request)
	if err != nil {
		t.Fatal(err)
	}
	record, err := GetCanonicalEvidenceByID(ctx, pool, admitted.CanonicalRef)
	if err != nil || record.NodeKind != evidencegraph.CanonicalCandidate || record.Payload.SourceType != "candidate" {
		t.Fatalf("candidate record: %+v %v", record, err)
	}
	if !reflect.DeepEqual(admitted.ParentNodeIDs, []string{parent}) || admitted.DerivationID == "" {
		t.Fatal("candidate lost its complete derivation")
	}
	replay, err := AdmitPendingProposal(ctx, pool, request)
	if err != nil || !replay.Replayed || replay.CanonicalRef != admitted.CanonicalRef {
		t.Fatalf("exact candidate replay: %+v %v", replay, err)
	}
	t.Run("reject-mode-drift", func(t *testing.T) {
		changed := request
		d := DerivationAdmissionInput(*request.Candidate)
		changed.Candidate = nil
		changed.Derivation = &d
		if _, err := AdmitPendingProposal(ctx, pool, changed); err == nil {
			t.Fatal("candidate replay became derived")
		}
		changed.Candidate = request.Candidate
		if _, err := AdmitPendingProposal(ctx, pool, changed); err == nil {
			t.Fatal("replay bypassed mutually exclusive modes")
		}
	})
	t.Run("reject-member", func(t *testing.T) {
		_, err := BindCanonicalProposition(ctx, pool, PropositionBindingInput{RequestID: "candidate-member", Key: propositionTestKey("candidate"), Definition: "not an established claim", NodeID: admitted.CanonicalRef, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "negative control"})
		if err == nil || !strings.Contains(err.Error(), "admitted source_claim or derived_claim") {
			t.Fatalf("candidate-kind rejection: %v", err)
		}
		assertTableCount(t, ctx, pool, "canonical_propositions", 0)
		assertTableCount(t, ctx, pool, "canonical_proposition_bindings", 0)
	})
	t.Run("reject-promotion-through-parent", func(t *testing.T) {
		child := AdmissionInput{ProposalOccurrenceID: pending("candidate-child"), DecisionBy: "TEST APPROVAL STUB", DecisionReason: "negative control", Derivation: &DerivationAdmissionInput{ParentNodeIDs: []string{admitted.CanonicalRef}, Method: "declared", Producer: "fixture", TraceRef: "trace:child"}}
		_, err := AdmitPendingProposal(ctx, pool, child)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23514" || !strings.Contains(pgerr.Message, "cannot promote a candidate parent") {
			t.Fatalf("candidate promotion rejection: %v", err)
		}
		got, err := GetProposalByOccurrenceID(ctx, pool, child.ProposalOccurrenceID)
		if err != nil || got.AdmissionOutcome != admissionOutcomePending || got.CanonicalRef != "" {
			t.Fatalf("failed promotion wrote authority: %+v %v", got, err)
		}
		assertTableCount(t, ctx, pool, "canonical_derivations", 1)
	})
}

func TestIntegrationContradictionSourceBinding(t *testing.T) {
	ctx, pool := integrationPool(t)
	a := propositionTestClaim(t, ctx, pool, "relation-a")
	b := propositionTestClaim(t, ctx, pool, "relation-b")
	rationale := "The supplied assertions have incompatible bounds."
	source := func(name, text string) string {
		t.Helper()
		r, e := IngestManualText(ctx, pool, ManualTextInput{SourceID: name, SourceVersion: "v1", Raw: []byte(text + "\n"), RequestID: name, AttemptNumber: 1}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "relation", StatementText: text, EvidenceRefs: []string{"span:S1"}}}})
		if e != nil {
			t.Fatal(e)
		}
		return r.ProposalOccurrenceID
	}
	origin := source("relation-source", rationale)
	other := source("other-relation-source", rationale)
	unrelated := source("unrelated-source", "Another statement.")
	input := CanonicalContradictionProposalInput{RequestID: "source-bound-relation", NodeAID: a, NodeBID: b, Rationale: rationale, ProducerName: "fixture", ProducerVersion: "v1", SourceProposalOccurrenceID: origin}
	before, err := SubmitCanonicalContradictionProposal(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := SubmitCanonicalContradictionProposal(ctx, pool, input)
	if err != nil || !replay.Replayed || replay.Proposal.ID != before.Proposal.ID {
		t.Fatalf("exact source replay: %+v %v", replay, err)
	}
	for _, id := range []string{other, unrelated, "occ:absent", ""} {
		changed := input
		changed.SourceProposalOccurrenceID = id
		if _, err := SubmitCanonicalContradictionProposal(ctx, pool, changed); err == nil {
			t.Errorf("changed/missing source was accepted: %s", id)
		}
	}
	read, err := GetCanonicalContradictionProposal(ctx, pool, before.Proposal.ID)
	if err != nil || read.SourceProposal == nil || read.SourceProposal.ProposalOccurrenceID != origin || len(read.SourceProposal.SourceRefs) != 1 || read.SourceProposal.SourceRefs[0].QuotedText != rationale {
		t.Fatalf("source readback: %+v %v", read, err)
	}
	// Direct SQL may not create a source-bound relation from unrelated material.
	_, insertErr := pool.Exec(ctx, `INSERT INTO canonical_contradiction_proposals
        (canonical_contradiction_proposal_id,request_id,request_payload_hash,proposal_fingerprint,node_a_id,node_b_id,relation,rationale,producer_name,producer_version,admission_outcome,source_proposal_occurrence_id)
        SELECT canonical_contradiction_proposal_id||'-invalid',request_id||'-invalid',request_payload_hash,proposal_fingerprint,node_a_id,node_b_id,relation,rationale,producer_name,producer_version,admission_outcome,$2
        FROM canonical_contradiction_proposals WHERE canonical_contradiction_proposal_id=$1`, before.Proposal.ID, unrelated)
	var sourceErr *pgconn.PgError
	if !errors.As(insertErr, &sourceErr) || sourceErr.Code != "23514" || !strings.Contains(sourceErr.Message, "grounded statement matching") {
		t.Fatalf("direct SQL source validation: %v", insertErr)
	}
	// Existing occurrence provenance cannot be retargeted behind a fixed source ID.
	if _, err := pool.Exec(ctx, `UPDATE proposal_occurrences SET source_refs='[]'::jsonb WHERE proposal_occurrence_id=$1`, origin); err == nil {
		t.Fatal("relation source occurrence changed its grounding")
	}
	var originalRow string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM canonical_contradiction_proposals p WHERE canonical_contradiction_proposal_id=$1`, before.Proposal.ID).Scan(&originalRow); err != nil {
		t.Fatal(err)
	}
	// The immutable binding must also reject direct SQL, even for the database owner.
	for _, sql := range []string{
		`UPDATE canonical_contradiction_proposals SET source_proposal_occurrence_id=NULL WHERE canonical_contradiction_proposal_id=$1`,
		`UPDATE canonical_contradiction_proposals SET rationale='changed' WHERE canonical_contradiction_proposal_id=$1`,
		`UPDATE canonical_contradiction_proposals SET node_a_id=node_b_id WHERE canonical_contradiction_proposal_id=$1`,
		`UPDATE canonical_contradiction_proposals SET node_b_id=node_a_id WHERE canonical_contradiction_proposal_id=$1`,
		`UPDATE canonical_contradiction_proposals SET producer_name='changed' WHERE canonical_contradiction_proposal_id=$1`,
		`UPDATE canonical_contradiction_proposals SET request_payload_hash='changed' WHERE canonical_contradiction_proposal_id=$1`,
		`UPDATE canonical_contradiction_proposals SET source_proposal_occurrence_id=(SELECT proposal_occurrence_id FROM proposal_occurrences WHERE statement_text='Another statement.' LIMIT 1) WHERE canonical_contradiction_proposal_id=$1`,
	} {
		_, err := pool.Exec(ctx, sql, before.Proposal.ID)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23514" || pgerr.Message != "relation source binding is immutable" {
			t.Fatalf("SQL binding mutation accepted or wrong failure: %v", err)
		}
		var afterRow string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM canonical_contradiction_proposals p WHERE canonical_contradiction_proposal_id=$1`, before.Proposal.ID).Scan(&afterRow); err != nil || afterRow != originalRow {
			t.Fatalf("rejected mutation changed record: %v", err)
		}
	}
	c := propositionTestClaim(t, ctx, pool, "legacy-relation-c")
	legacyInput := input
	legacyInput.RequestID, legacyInput.NodeBID, legacyInput.SourceProposalOccurrenceID = "legacy-no-source", c, ""
	legacy, err := SubmitCanonicalContradictionProposal(ctx, pool, legacyInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE canonical_contradiction_proposals SET source_proposal_occurrence_id=$2 WHERE canonical_contradiction_proposal_id=$1`, legacy.Proposal.ID, origin)
	var nullErr *pgconn.PgError
	if !errors.As(err, &nullErr) || nullErr.Code != "23514" || nullErr.Message != "relation source binding is immutable" {
		t.Fatalf("legacy NULL source retargeted: %v", err)
	}
	decision := CanonicalContradictionAdmissionInput{ProposalID: before.Proposal.ID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic relation decision"}
	if _, err := AdmitPendingCanonicalContradiction(ctx, pool, decision); err != nil {
		t.Fatal(err)
	}
	terminal, err := GetCanonicalContradictionProposal(ctx, pool, before.Proposal.ID)
	if err != nil || terminal.Decision == nil || terminal.SourceProposal == nil || terminal.SourceProposal.ProposalOccurrenceID != origin {
		t.Fatalf("terminal source disappeared: %+v %v", terminal, err)
	}
}
