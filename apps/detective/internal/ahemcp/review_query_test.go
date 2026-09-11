package ahemcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestVerifyAdmittedExactQueryAndFirstMaterializer(t *testing.T) {
	r, doc, records, h := reviewFixture(t)
	a := fixtureAdmission(h)
	proposal := queryFixture(doc, records[0], h)
	canonical := canonicalReviewFixture(t, proposal, a)
	// A reused semantic node retains an earlier producer and occurrence. The
	// current proposal must still match the actual selected candidate exactly.
	canonical.Extractor.Name = "earlier-materializer"
	proposal.AdmissionOutcome = "admitted"
	proposal.CanonicalRef, _ = json.Marshal(a.CanonicalRef)
	launcher, _ := reviewLauncher(t, "query", r, a, &proposal, &canonical)
	if err := VerifyAdmitted(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records, h, a); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAdmittedRejectsQueryDrift(t *testing.T) {
	r, doc, records, h := reviewFixture(t)
	a := fixtureAdmission(h)
	base := queryFixture(doc, records[0], h)
	canonical := canonicalReviewFixture(t, base, a)
	mutations := map[string]any{
		"record_ref.kind": "proposal", "record_ref.id": "other", "canonical_ref": "canon-node:other", "admission_outcome": "pending",
		"statement_text": "other", "proposal_origin_ref": nil, "canonical": nil, "source.source_id": "other", "source.source_version": "other",
		"source.source_snapshot_id": "srcsnap:other", "source_refs": []any{}, "source.external_source": map[string]any{}, "proposal_kind": "code_fact",
	}
	for path, value := range mutations {
		t.Run(path, func(t *testing.T) {
			changed := mutateQueryFixture(t, canonical, path, value, false)
			if verifyCanonicalReadback(changed, base, a) == nil {
				t.Fatal("canonical drift accepted")
			}
		})
	}
	for _, path := range []string{"node_kind", "payload.claim", "payload.span", "payload.span_locator", "payload.source", "payload.source_type", "provenance.origin_group_id"} {
		t.Run(path, func(t *testing.T) {
			changed := canonical
			var info map[string]any
			_ = json.Unmarshal(changed.Canonical, &info)
			parts := strings.Split(path, ".")
			if len(parts) == 1 {
				info[parts[0]] = "other"
			} else {
				info[parts[0]].(map[string]any)[parts[1]] = "other"
			}
			changed.Canonical, _ = json.Marshal(info)
			if verifyCanonicalReadback(changed, base, a) == nil {
				t.Fatal("canonical body drift accepted")
			}
		})
	}
	for _, mode := range []string{"pending", "wrong-ref", "changed-source"} {
		t.Run(mode, func(t *testing.T) {
			proposal := base
			proposal.AdmissionOutcome = "admitted"
			proposal.CanonicalRef, _ = json.Marshal(a.CanonicalRef)
			switch mode {
			case "pending":
				proposal.AdmissionOutcome = "pending"
			case "wrong-ref":
				proposal.CanonicalRef = []byte(`"canon-node:other"`)
			case "changed-source":
				proposal.Source.SourceID = "other"
			}
			launcher, _ := reviewLauncher(t, "query", r, a, &proposal, &canonical)
			if VerifyAdmitted(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records, h, a) == nil {
				t.Fatal("wrong admitted occurrence accepted")
			}
		})
	}
}

func canonicalReviewFixture(t *testing.T, proposal pendingRecord, a ReviewedAdmission) pendingRecord {
	t.Helper()
	result := proposal
	result.RecordRef.Kind, result.RecordRef.ID = "canonical_evidence", a.CanonicalRef
	result.AdmissionOutcome = "admitted"
	result.CanonicalRef, _ = json.Marshal(a.CanonicalRef)
	result.ProposalOriginRef, _ = json.Marshal(map[string]string{"kind": "proposal", "id": "occ:" + strings.Repeat("b", 64)})
	quotes, locators := []string{}, []string{}
	for _, ref := range proposal.SourceRefs {
		quotes = append(quotes, ref.QuotedText)
		locators = append(locators, fmt.Sprintf("%s#%s[%d:%d]", ref.ExtractionViewID, ref.SpanID, *ref.StartByte, *ref.EndByte))
	}
	result.Canonical, _ = json.Marshal(map[string]any{
		"node_kind":  "source_claim",
		"payload":    map[string]any{"id": "payload:" + strings.Repeat("a", 16), "source_type": "manual_text", "title": "source claim", "source": "manual_text:" + proposal.Source.SourceID + "@" + proposal.Source.SourceVersion, "span": strings.Join(quotes, "\n"), "span_locator": strings.Join(locators, "\n"), "claim": proposal.StatementText},
		"provenance": map[string]any{"origin_group_id": proposal.Source.SourceSnapshotID, "origin_refs": append([]string{proposal.Source.SourceSnapshotID, proposal.ExtractionViewID}, locators...)},
	})
	return result
}

func TestReviewedAdmissionRejectsAuthorityDrift(t *testing.T) {
	_, _, _, h := reviewFixture(t)
	original := fixtureAdmission(h)
	for _, mode := range []string{"occurrence", "decision", "outcome", "canonical", "raw", "edge", "duplicate", "derivation", "parents"} {
		t.Run(mode, func(t *testing.T) {
			a := original
			switch mode {
			case "occurrence":
				a.ProposalOccurrenceID = "other"
			case "decision":
				a.AdmissionDecisionID = "other"
			case "outcome":
				a.AdmissionOutcome = "pending"
			case "canonical":
				a.CanonicalRef = fixtureID("canon-node:")
			case "raw":
				a.RawEvidenceNodeIDs = []string{}
			case "edge":
				a.CanonicalEdgeIDs = []string{}
			case "duplicate":
				a.RawEvidenceNodeIDs = []string{a.CanonicalRef}
			case "derivation":
				a.DerivationID = "other"
			case "parents":
				a.ParentNodeIDs = []string{"other"}
			}
			if validateReviewedAdmission(a, h.ProposalOccurrenceID, 1) == nil {
				t.Fatal("admission drift accepted")
			}
		})
	}
}
