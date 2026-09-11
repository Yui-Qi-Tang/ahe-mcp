package ahemcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// VerifyAdmitted checks the selected occurrence and its canonical claim through
// the independent read-only Query launcher. Query does not expose the persisted
// review binding; this verifies evidence/lifecycle, not reviewer authentication.
func VerifyAdmitted(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, handoff Handoff, admission ReviewedAdmission) error {
	if ctx == nil {
		return errors.New("admitted Query context is required")
	}
	if err := validateReviewInputs(sourceID, document, extractor, records, handoff); err != nil {
		return err
	}
	if err := validateReviewedAdmission(admission, handoff.ProposalOccurrenceID, records[0].Citation.EndLine-records[0].Citation.StartLine+1); err != nil {
		return err
	}
	c, err := startQuery(ctx, command)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return err
	}
	var proposal pendingRecord
	if err := c.call("get_evidence_record", map[string]string{"proposal_occurrence_id": handoff.ProposalOccurrenceID}, &proposal); err != nil {
		return err
	}
	var canonicalRef string
	if json.Unmarshal(proposal.CanonicalRef, &canonicalRef) != nil || canonicalRef != admission.CanonicalRef || proposal.AdmissionOutcome != "admitted" {
		return errors.New("Query did not verify the exact admitted occurrence")
	}
	proposal.AdmissionOutcome, proposal.CanonicalRef = "pending", json.RawMessage("null")
	if err := verifyPendingRecord(proposal, sourceID, document, extractor, records[0], handoff); err != nil {
		return err
	}
	var canonical pendingRecord
	if err := c.call("get_evidence_record", map[string]string{"canonical_id": admission.CanonicalRef}, &canonical); err != nil {
		return err
	}
	if err := verifyCanonicalReadback(canonical, proposal, admission); err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return errors.New("admitted Query launcher did not exit cleanly")
	}
	return nil
}

func verifyCanonicalReadback(record, proposal pendingRecord, admission ReviewedAdmission) error {
	bad := func() error {
		return errors.New("canonical Query record differs from the admitted claim and exact source")
	}
	var ref string
	if json.Unmarshal(record.CanonicalRef, &ref) != nil || ref != admission.CanonicalRef || record.RecordRef.Kind != "canonical_evidence" || record.RecordRef.ID != ref || record.AdmissionOutcome != "admitted" || record.StatementText != proposal.StatementText {
		return bad()
	}
	var origin struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if decodeRPCJSON(record.ProposalOriginRef, &origin, true) != nil || origin.Kind != "proposal" || !reviewID(origin.ID, "occ:") {
		return bad()
	}
	// Semantic reuse preserves the first materializer. Its occurrence/extractor
	// may differ; its source-bound claim and exact span material must not.
	var info struct {
		NodeKind string `json:"node_kind"`
		Payload  struct {
			ID            string            `json:"id"`
			SourceType    string            `json:"source_type"`
			Title         string            `json:"title"`
			Source        string            `json:"source"`
			Span          string            `json:"span"`
			SpanLocator   string            `json:"span_locator"`
			Claim         string            `json:"claim"`
			Applicability string            `json:"applicability,omitempty"`
			TargetAnchors []json.RawMessage `json:"target_anchors,omitempty"`
		} `json:"payload"`
		Provenance struct {
			OriginGroupID string   `json:"origin_group_id"`
			OriginRefs    []string `json:"origin_refs"`
		} `json:"provenance"`
	}
	if decodeRPCJSON(record.Canonical, &info, false) != nil || info.NodeKind != "source_claim" || !hexID(info.Payload.ID, "payload:", 16) || info.Payload.SourceType != "manual_text" || info.Payload.Title != "source claim" || info.Payload.Claim != proposal.StatementText || info.Payload.Source != "manual_text:"+proposal.Source.SourceID+"@"+proposal.Source.SourceVersion || info.Payload.Applicability != "" || len(info.Payload.TargetAnchors) != 0 || info.Provenance.OriginGroupID != proposal.Source.SourceSnapshotID {
		return bad()
	}
	quotes, locators := []string{}, []string{}
	for _, ref := range proposal.SourceRefs {
		quotes = append(quotes, ref.QuotedText)
		locators = append(locators, fmt.Sprintf("%s#%s[%d:%d]", ref.ExtractionViewID, ref.SpanID, *ref.StartByte, *ref.EndByte))
	}
	origins := append([]string{proposal.Source.SourceSnapshotID, proposal.ExtractionViewID}, locators...)
	if info.Payload.Span != strings.Join(quotes, "\n") || info.Payload.SpanLocator != strings.Join(locators, "\n") || !slices.Equal(info.Provenance.OriginRefs, origins) {
		return bad()
	}
	// Compare source material using the same established pending checker. Only
	// first-materializer producer fields and lifecycle identity are substituted.
	record.RecordRef = proposal.RecordRef
	record.ProposalLocalID = proposal.ProposalLocalID
	record.Extractor = proposal.Extractor
	record.AdmissionOutcome, record.CanonicalRef = "pending", json.RawMessage("null")
	record.Canonical, record.ProposalOriginRef = nil, nil
	left, _ := json.Marshal(record)
	right, _ := json.Marshal(proposal)
	if !sameJSON(left, right) {
		return bad()
	}
	return nil
}
