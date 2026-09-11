package pending

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

const (
	reviewBundleVersion   = "detective-source-review/v1"
	reviewDecisionVersion = "detective-review-decision/v1"
	maxReviewFileBytes    = 12 << 20
)

// ErrReviewDecisionNotExecutable means the saved intent has no permitted writer
// in the current profile; no MCP process is started for that intent.
var ErrReviewDecisionNotExecutable = errors.New("pending is a local pause, not an executable review decision")

// ReviewBundle preserves the complete MCP display alongside its exact local
// source and prior pending locator. Its digest is integrity, not authentication.
type ReviewBundle struct {
	SchemaVersion string                   `json:"schema_version"`
	Checkpoint    Checkpoint               `json:"checkpoint"`
	Receipt       Result                   `json:"receipt"`
	Review        ahemcp.SourceClaimReview `json:"review"`
	Digest        string                   `json:"digest"`
}

type reviewBundlePayload struct {
	SchemaVersion string                   `json:"schema_version"`
	Checkpoint    Checkpoint               `json:"checkpoint"`
	Receipt       Result                   `json:"receipt"`
	Review        ahemcp.SourceClaimReview `json:"review"`
}

func (b ReviewBundle) payload() reviewBundlePayload {
	return reviewBundlePayload{b.SchemaVersion, b.Checkpoint, b.Receipt, b.Review}
}

// ReviewDecision records an operator-supplied intent against an exact display.
// Even an admit decision in this private file does not mutate AHE or prove that
// a human read the display. Only ApplyReviewDecision can invoke the writer.
type ReviewDecision struct {
	SchemaVersion      string       `json:"schema_version"`
	ReviewBundle       ReviewBundle `json:"review_bundle"`
	Decision           string       `json:"decision"`
	Reason             string       `json:"reason"`
	ConfirmedDisplayID string       `json:"confirmed_display_id"`
	Digest             string       `json:"digest"`
}

type reviewDecisionPayload struct {
	SchemaVersion      string       `json:"schema_version"`
	ReviewBundle       ReviewBundle `json:"review_bundle"`
	Decision           string       `json:"decision"`
	Reason             string       `json:"reason"`
	ConfirmedDisplayID string       `json:"confirmed_display_id"`
}

func (d ReviewDecision) payload() reviewDecisionPayload {
	return reviewDecisionPayload{d.SchemaVersion, d.ReviewBundle, d.Decision, d.Reason, d.ConfirmedDisplayID}
}

// ReviewExecution reports only an acknowledged admission with exact Query
// readback in this invocation. Query does not return a review-binding receipt.
type ReviewExecution struct {
	SchemaVersion    string                   `json:"schema_version"`
	State            string                   `json:"state"`
	DecisionDigest   string                   `json:"decision_digest"`
	Admission        ahemcp.ReviewedAdmission `json:"admission,omitzero"`
	Disposition      *ReviewDispositionResult `json:"disposition,omitempty"`
	ReadbackVerified bool                     `json:"readback_verified"`
	HumanReview      string                   `json:"human_review"`
}

// ReviewDispositionResult reports the acknowledged outcome without echoing the
// sensitive reason from the native response to ordinary command output.
type ReviewDispositionResult struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	AdmissionDecisionID  string `json:"admission_decision_id"`
	AdmissionOutcome     string `json:"admission_outcome"`
	Replayed             bool   `json:"replayed"`
}

// PrepareReview checks the pending occurrence through Query, obtains the full
// formal review through the selected reviewer, and saves it without approval.
// A reserved output is never replaced, including on retry or RPC failure.
func PrepareReview(ctx context.Context, checkpointPath, receiptPath, reviewCommand, queryCommand, outPath string) (ReviewBundle, error) {
	if err := ctx.Err(); err != nil {
		return ReviewBundle{}, err
	}
	if strings.TrimSpace(reviewCommand) == "" || strings.TrimSpace(queryCommand) == "" {
		return ReviewBundle{}, errors.New("review preparation requires explicit reviewer and Query launchers")
	}
	checkpoint, err := Load(checkpointPath)
	if err != nil {
		return ReviewBundle{}, err
	}
	receipt, err := loadInspectionReceipt(receiptPath, checkpoint)
	if err != nil {
		return ReviewBundle{}, err
	}
	document, err := checkpoint.Document()
	if err != nil {
		return ReviewBundle{}, err
	}
	w, err := Reserve(outPath)
	if err != nil {
		return ReviewBundle{}, fmt.Errorf("reserve new private review file: %w", err)
	}
	defer w.Close()
	records := checkpoint.Batch.Rows[0].Result.Records
	if err := ahemcp.VerifyPending(ctx, queryCommand, checkpoint.SourceID, document, checkpoint.Batch.Extractor, records, receipt.Handoff); err != nil {
		return ReviewBundle{}, fmt.Errorf("review preparation pending readback failed; no approval attempted: %w", err)
	}
	review, err := ahemcp.ReviewSourceClaim(ctx, reviewCommand, checkpoint.SourceID, document, checkpoint.Batch.Extractor, records, receipt.Handoff)
	if err != nil {
		return ReviewBundle{}, fmt.Errorf("formal review unavailable; no approval attempted: %w", err)
	}
	bundle := ReviewBundle{SchemaVersion: reviewBundleVersion, Checkpoint: checkpoint, Receipt: receipt, Review: review}
	bundle.Digest, err = reviewHash(bundle.payload())
	if err != nil {
		return ReviewBundle{}, err
	}
	if err := bundle.validate(); err != nil {
		return ReviewBundle{}, err
	}
	if err := writeReviewFile(w, bundle); err != nil {
		return ReviewBundle{}, err
	}
	return bundle, nil
}

// LoadReview reads a complete private review without any source/model/MCP I/O.
func LoadReview(path string) (ReviewBundle, error) {
	var bundle ReviewBundle
	if err := readReviewFile(path, &bundle); err != nil {
		return ReviewBundle{}, err
	}
	if err := bundle.validate(); err != nil {
		return ReviewBundle{}, err
	}
	return bundle, nil
}

func (b ReviewBundle) validate() error {
	digest, err := reviewHash(b.payload())
	if err != nil || b.SchemaVersion != reviewBundleVersion || digest != b.Digest {
		return errors.New("review bundle version or exact-input digest mismatch")
	}
	document, err := b.Checkpoint.Document()
	if err != nil {
		return err
	}
	body, err := json.Marshal(b.Receipt)
	if err != nil {
		return errors.New("invalid saved review receipt")
	}
	if _, err := decodeInspectionReceipt(body, b.Checkpoint); err != nil {
		return err
	}
	return ahemcp.ValidateSourceClaimReview(b.Review, b.Checkpoint.SourceID, document,
		b.Checkpoint.Batch.Extractor, b.Checkpoint.Batch.Rows[0].Result.Records, b.Receipt.Handoff)
}

// RecordReviewDecision validates explicit input and durably saves it locally.
// No default, source text, model suggestion or previous conversation supplies
// the decision. Saving any decision does not execute it.
func RecordReviewDecision(bundle ReviewBundle, decision, reason, confirmedDisplayID, outPath string) (ReviewDecision, error) {
	if err := bundle.validate(); err != nil {
		return ReviewDecision{}, err
	}
	d := ReviewDecision{SchemaVersion: reviewDecisionVersion, ReviewBundle: bundle,
		Decision: decision, Reason: reason, ConfirmedDisplayID: confirmedDisplayID}
	var err error
	d.Digest, err = reviewHash(d.payload())
	if err != nil {
		return ReviewDecision{}, err
	}
	if err := d.validate(); err != nil {
		return ReviewDecision{}, err
	}
	w, err := Reserve(outPath)
	if err != nil {
		return ReviewDecision{}, fmt.Errorf("reserve new private decision file: %w", err)
	}
	defer w.Close()
	if err := writeReviewFile(w, d); err != nil {
		return ReviewDecision{}, err
	}
	return d, nil
}

func (d ReviewDecision) validate() error {
	digest, err := reviewHash(d.payload())
	if err != nil || d.SchemaVersion != reviewDecisionVersion || digest != d.Digest {
		return errors.New("review decision version or exact-input digest mismatch")
	}
	if err := d.ReviewBundle.validate(); err != nil {
		return err
	}
	switch d.Decision {
	case "admit", "audit_only", "reject", "pending":
	default:
		return errors.New("review decision must explicitly be admit, audit_only, reject, or pending")
	}
	if d.Reason == "" || strings.TrimSpace(d.Reason) != d.Reason || len(d.Reason) > 2000 || !utf8.ValidString(d.Reason) || strings.ContainsRune(d.Reason, '\x00') {
		return errors.New("review reason must be nonempty normalized UTF-8 of at most 2000 bytes without NUL")
	}
	if d.ConfirmedDisplayID != d.ReviewBundle.Review.Display.ID {
		return errors.New("decision requires explicit confirmation of the complete review display ID")
	}
	return nil
}

// ApplyReviewDecision invokes only the saved explicit decision, then independently
// verifies its exact evidence. Failures can follow a committed admission: retain
// and retry the same decision file, subject, reason and operator-owned launchers.
// A pending-only review reload would incorrectly prevent post-commit recovery.
func ApplyReviewDecision(ctx context.Context, decisionPath, reviewCommand, queryCommand, confirmedDisplayID string) (ReviewExecution, error) {
	if err := ctx.Err(); err != nil {
		return ReviewExecution{}, err
	}
	decision, err := LoadReviewDecision(decisionPath)
	if err != nil {
		return ReviewExecution{}, err
	}
	if confirmedDisplayID != decision.ConfirmedDisplayID {
		return ReviewExecution{}, errors.New("apply requires explicit confirmation of the saved review display ID")
	}
	if decision.Decision == "pending" {
		return ReviewExecution{}, ErrReviewDecisionNotExecutable
	}
	if strings.TrimSpace(reviewCommand) == "" || strings.TrimSpace(queryCommand) == "" {
		return ReviewExecution{}, errors.New("review apply requires explicit reviewer and Query launchers")
	}
	bundle := decision.ReviewBundle
	document, err := bundle.Checkpoint.Document()
	if err != nil {
		return ReviewExecution{}, err
	}
	if decision.Decision != "admit" {
		disposition, err := ahemcp.DisposeSourceClaim(ctx, reviewCommand, bundle.Review, decision.Decision, decision.Reason)
		if err != nil {
			return ReviewExecution{}, fmt.Errorf("disposition outcome unconfirmed; retain the exact saved decision for retry: %w", err)
		}
		if err := ahemcp.VerifyDisposed(ctx, queryCommand, bundle.Checkpoint.SourceID, document,
			bundle.Checkpoint.Batch.Extractor, bundle.Checkpoint.Batch.Rows[0].Result.Records, bundle.Receipt.Handoff, disposition); err != nil {
			return ReviewExecution{}, fmt.Errorf("disposition acknowledged but Query readback unverified; retain the exact saved decision: %w", err)
		}
		return ReviewExecution{SchemaVersion: "detective-review-execution/v2", State: disposition.AdmissionOutcome + "_verified",
			DecisionDigest: decision.Digest, Disposition: &ReviewDispositionResult{disposition.ProposalOccurrenceID,
				disposition.AdmissionDecisionID, disposition.AdmissionOutcome, disposition.Replayed},
			ReadbackVerified: true, HumanReview: "operator_supplied_not_authenticated"}, nil
	}
	admission, err := ahemcp.AdmitSourceClaim(ctx, reviewCommand, bundle.Review, decision.Reason)
	if err != nil {
		return ReviewExecution{}, fmt.Errorf("admission outcome unconfirmed; retain and retry the exact saved decision with the same approved launchers: %w", err)
	}
	if err := ahemcp.VerifyAdmitted(ctx, queryCommand, bundle.Checkpoint.SourceID, document,
		bundle.Checkpoint.Batch.Extractor, bundle.Checkpoint.Batch.Rows[0].Result.Records, bundle.Receipt.Handoff, admission); err != nil {
		return ReviewExecution{}, fmt.Errorf("admission acknowledged but Query readback unverified; retain and retry the exact saved decision: %w", err)
	}
	return ReviewExecution{SchemaVersion: "detective-review-execution/v1", State: "admitted_verified",
		DecisionDigest: decision.Digest, Admission: admission, ReadbackVerified: true,
		HumanReview: "operator_supplied_not_authenticated"}, nil
}

// LoadReviewDecision validates a saved private decision without source, model,
// launcher, or database I/O. It does not imply execution or authentication.
func LoadReviewDecision(path string) (ReviewDecision, error) {
	var decision ReviewDecision
	if err := readReviewFile(path, &decision); err != nil {
		return ReviewDecision{}, err
	}
	if err := decision.validate(); err != nil {
		return ReviewDecision{}, err
	}
	return decision, nil
}

func reviewHash(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > maxReviewFileBytes {
		return "", errors.New("review artifact exceeds its encoding bound")
	}
	return checkpointHash(body), nil
}

func writeReviewFile(w *Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > maxReviewFileBytes {
		return errors.New("review artifact exceeds its encoding bound")
	}
	if err := w.writeBody(body); err != nil {
		return fmt.Errorf("save immutable private review artifact: %w", err)
	}
	return w.Close()
}

func readReviewFile(path string, output any) error {
	root, directory, name, err := checkpointParent(path)
	if err != nil {
		return errors.New("review artifact requires a clean absolute path in a private directory")
	}
	defer root.Close()
	defer directory.Close()
	file, err := privateOpen(root, name, os.O_RDONLY, 0)
	if err != nil {
		return errors.New("review artifact is not a readable private regular file")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxReviewFileBytes+1))
	if err != nil || len(body) > maxReviewFileBytes || !utf8.Valid(body) {
		return errors.New("review artifact is unreadable, oversized, or invalid UTF-8")
	}
	check := json.NewDecoder(bytes.NewReader(body))
	if uniqueJSON(check, 0) != nil {
		return errors.New("review artifact contains malformed or duplicate JSON fields")
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("review artifact contains trailing JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil {
		return errors.New("review artifact does not match its closed JSON schema")
	}
	canonical, err := json.Marshal(output)
	var observed, expected any
	if err != nil || json.Unmarshal(body, &observed) != nil || json.Unmarshal(canonical, &expected) != nil || !reflect.DeepEqual(observed, expected) {
		return errors.New("review artifact contains missing or noncanonical schema fields")
	}
	return nil
}
