package ahemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// CandidateLocator identifies one independently submitted single-candidate
// attempt. SubmissionOutcome is a historical observation, never admission
// authority. A Detective batch consists of several such native attempts.
type CandidateLocator struct {
	SourceSnapshotID     string `json:"source_snapshot_id"`
	ExtractionViewID     string `json:"extraction_view_id"`
	ExtractionAttemptID  string `json:"extraction_attempt_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	SubmissionOutcome    string `json:"submission_outcome"`
	Replayed             bool   `json:"replayed"`
}

// CandidateObservation is an exact source-bound lifecycle read, not a review
// decision receipt or a snapshot of all candidates at one database instant.
type CandidateObservation struct {
	AdmissionOutcome string `json:"admission_outcome"`
	CanonicalRef     string `json:"canonical_ref,omitempty"`
}

// SubmitBatchCandidate submits exactly one frozen candidate. An exact retry
// may discover that a separate reviewer already made it terminal. It does not
// perform admission, change that decision, or accept only the first member of a
// multi-proposal native response.
func SubmitBatchCandidate(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record) (CandidateLocator, error) {
	if ctx == nil || len(records) != 1 {
		return CandidateLocator{}, errors.New("batch candidate submission requires a context and exactly one saved candidate")
	}
	if err := ValidateSubmission(sourceID, document, extractor, records); err != nil {
		return CandidateLocator{}, err
	}
	h, err := submitValidatedState(ctx, command, sourceID, document, extractor, records, true)
	if err != nil {
		return CandidateLocator{}, err
	}
	locator := CandidateLocator{h.SourceSnapshotID, h.ExtractionViewID, h.ExtractionAttemptID, h.ProposalOccurrenceID, h.Status, h.Replayed}
	if err := ValidateCandidateLocator(locator); err != nil {
		return CandidateLocator{}, err
	}
	return locator, nil
}

// ValidateCandidateLocator rejects incomplete or unsupported receipt identities.
func ValidateCandidateLocator(locator CandidateLocator) error {
	if locator.SubmissionOutcome != "pending" && (!locator.Replayed || !candidateTerminalState(locator.SubmissionOutcome)) {
		return errors.New("candidate locator has an unsupported submission outcome")
	}
	for _, v := range []struct{ value, prefix string }{{locator.SourceSnapshotID, "srcsnap:"}, {locator.ExtractionViewID, "view:"}, {locator.ExtractionAttemptID, "attempt:"}, {locator.ProposalOccurrenceID, "occ:"}} {
		if !reviewID(v.value, v.prefix) {
			return errors.New("candidate locator requires complete exact native identities")
		}
	}
	return nil
}

func candidateTerminalState(state string) bool {
	return state == "admitted" || state == "rejected" || state == "audit_only"
}

// ObserveBatchCandidate reads one known occurrence through Query without any
// intake fallback. Terminal outcomes are verified, not converted into pending.
func ObserveBatchCandidate(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, locator CandidateLocator) (CandidateObservation, error) {
	if ctx == nil || len(records) != 1 {
		return CandidateObservation{}, errors.New("candidate observation requires a context and one saved candidate")
	}
	if err := ValidateSubmission(sourceID, document, extractor, records); err != nil {
		return CandidateObservation{}, err
	}
	if err := ValidateCandidateLocator(locator); err != nil {
		return CandidateObservation{}, err
	}
	c, err := startQuery(ctx, command)
	if err != nil {
		return CandidateObservation{}, err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return CandidateObservation{}, err
	}
	var result pendingRecord
	if err := c.call("get_evidence_record", map[string]string{"proposal_occurrence_id": locator.ProposalOccurrenceID}, &result); err != nil {
		return CandidateObservation{}, err
	}
	observation, err := verifyBatchCandidateRecord(result, sourceID, document, extractor, records[0], locator)
	if err != nil {
		return CandidateObservation{}, err
	}
	if err := c.Close(); err != nil {
		return CandidateObservation{}, errors.New("candidate Query launcher did not exit cleanly")
	}
	return observation, nil
}

func verifyBatchCandidateRecord(result pendingRecord, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, record labstatus.Record, locator CandidateLocator) (CandidateObservation, error) {
	state := result.AdmissionOutcome
	if state != "pending" && !candidateTerminalState(state) {
		return CandidateObservation{}, errors.New("candidate Query returned an unsupported lifecycle")
	}
	if locator.SubmissionOutcome != "pending" && state != locator.SubmissionOutcome {
		return CandidateObservation{}, errors.New("candidate Query differs from its terminal submission observation")
	}
	observation := CandidateObservation{AdmissionOutcome: state}
	if state == "admitted" {
		if json.Unmarshal(result.CanonicalRef, &observation.CanonicalRef) != nil || !hexID(observation.CanonicalRef, "canon-node:", 16) {
			return CandidateObservation{}, errors.New("admitted candidate Query requires a canonical reference")
		}
	} else if !bytes.Equal(bytes.TrimSpace(result.CanonicalRef), []byte("null")) {
		return CandidateObservation{}, errors.New("non-admitted candidate Query must have no canonical reference")
	}
	// Only lifecycle fields are projected; the existing strict pending checker
	// still validates every source/candidate/producer field and rejects foreign
	// authority. Its public pending-only contract remains unchanged.
	result.AdmissionOutcome, result.CanonicalRef = "pending", json.RawMessage("null")
	h := Handoff{SchemaVersion: "ahe-mcp-pending-handoff/v0", SourceSnapshotID: locator.SourceSnapshotID, ExtractionViewID: locator.ExtractionViewID, ExtractionAttemptID: locator.ExtractionAttemptID, ProposalOccurrenceID: locator.ProposalOccurrenceID, ProposalCount: 1, Status: "pending", Replayed: locator.Replayed}
	if err := verifyPendingRecord(result, sourceID, document, extractor, record, h); err != nil {
		return CandidateObservation{}, err
	}
	return observation, nil
}
