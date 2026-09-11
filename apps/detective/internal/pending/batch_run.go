package pending

import (
	"context"
	"errors"
	"os"
	"reflect"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

// BatchResult includes every indexed member, even when recovery stops early.
// Each Query observation is independent; this is not one database snapshot.
type BatchResult struct {
	SchemaVersion        string              `json:"schema_version"`
	IndexDigest          string              `json:"index_digest"`
	NativeSubmissionMode string              `json:"native_submission_mode"`
	State                string              `json:"state"`
	AllCandidatesChecked bool                `json:"all_candidates_checked"`
	AuthorityEffect      string              `json:"authority_effect"`
	Members              []BatchMemberResult `json:"members"`
	Summary              BatchSummary        `json:"summary"`
}

// BatchMemberResult keeps checkpoint, historical submission and current Query
// state separate. ReceiptPath is usable only when PendingReceiptAvailable is true.
type BatchMemberResult struct {
	Ordinal                 int                          `json:"ordinal"`
	CandidateDigest         string                       `json:"candidate_digest"`
	CheckpointDigest        string                       `json:"checkpoint_digest"`
	CheckpointPath          string                       `json:"checkpoint_path"`
	LocatorPath             string                       `json:"locator_path"`
	ReceiptPath             string                       `json:"receipt_path"`
	CheckpointAvailable     bool                         `json:"checkpoint_available"`
	PendingReceiptAvailable bool                         `json:"pending_receipt_available"`
	State                   string                       `json:"state"`
	FailureStage            string                       `json:"failure_stage,omitempty"`
	SubmissionAttempted     bool                         `json:"submission_attempted"`
	Locator                 *ahemcp.CandidateLocator     `json:"locator,omitempty"`
	Observation             *ahemcp.CandidateObservation `json:"observation,omitempty"`
}

// BatchSummary counts explicit per-candidate outcomes, not aggregate approval.
type BatchSummary struct {
	Total              int `json:"total"`
	SubmissionAttempts int `json:"submission_attempts"`
	Pending            int `json:"pending"`
	Admitted           int `json:"admitted"`
	Rejected           int `json:"rejected"`
	AuditOnly          int `json:"audit_only"`
	Failed             int `json:"failed"`
	NotAttempted       int `json:"not_attempted"`
	NotChecked         int `json:"not_checked"`
}

type batchLocatorFile struct {
	SchemaVersion    string                  `json:"schema_version"`
	CheckpointDigest string                  `json:"checkpoint_digest"`
	Locator          ahemcp.CandidateLocator `json:"locator"`
}

func newBatchResult(path string, index BatchIndex) BatchResult {
	result := BatchResult{SchemaVersion: "detective-candidate-batch-result/v1", IndexDigest: index.Digest, NativeSubmissionMode: batchSubmissionMode, State: "incomplete", AuthorityEffect: "no_admission_or_disposition", Members: make([]BatchMemberResult, 0, len(index.Members))}
	for _, member := range index.Members {
		cp, locator, receipt := batchMemberPaths(path, member.Ordinal)
		result.Members = append(result.Members, BatchMemberResult{Ordinal: member.Ordinal, CandidateDigest: member.CandidateDigest, CheckpointDigest: member.CheckpointDigest, CheckpointPath: cp, LocatorPath: locator, ReceiptPath: receipt, State: "not_attempted"})
	}
	return result
}

func (result *BatchResult) summarize() {
	summary := BatchSummary{Total: len(result.Members)}
	for _, member := range result.Members {
		if member.SubmissionAttempted {
			summary.SubmissionAttempts++
		}
		switch member.State {
		case "pending_verified":
			summary.Pending++
		case "admitted_verified":
			summary.Admitted++
		case "rejected_verified":
			summary.Rejected++
		case "audit_only_verified":
			summary.AuditOnly++
		case "failed":
			summary.Failed++
		case "not_attempted":
			summary.NotAttempted++
		default:
			summary.NotChecked++
		}
	}
	result.Summary = summary
	result.AllCandidatesChecked = summary.Pending+summary.Admitted+summary.Rejected+summary.AuditOnly == summary.Total
	if result.AllCandidatesChecked {
		result.State = "all_candidates_observed"
	}
}

// ResumeBatch recovers each independently indexed native attempt. It never
// reruns extraction, resubmits an acknowledged candidate, overwrites receipts,
// or sends an admission/disposition. On member failure it returns the complete
// explicit partial report with an error; later members stay not_attempted.
func ResumeBatch(ctx context.Context, path, intakeCommand, queryCommand string) (BatchResult, error) {
	if ctx == nil || intakeCommand == "" || queryCommand == "" {
		return BatchResult{}, errors.New("batch recovery requires context and explicit intake/Query launchers")
	}
	index, err := LoadBatchIndex(path)
	if err != nil {
		return BatchResult{}, err
	}
	document, err := index.Document()
	if err != nil {
		return BatchResult{}, err
	}
	// Retain this sidecar inode just like checkpoint reservations. The marker
	// path itself is never published, so normal future resumes can acquire it.
	lock, err := Reserve(path + ".recovery")
	if err != nil {
		return BatchResult{}, err
	}
	defer lock.Close()
	result := newBatchResult(path, index)
	for i := range result.Members {
		member := &result.Members[i]
		checkpoint, err := batchMemberCheckpoint(index, document, member.Ordinal)
		if err != nil {
			return batchFailure(result, i, "checkpoint_binding")
		}
		if err := ensureBatchCheckpoint(member.CheckpointPath, checkpoint); err != nil {
			return batchFailure(result, i, "checkpoint_publication")
		}
		member.CheckpointAvailable = true
		if err := ctx.Err(); err != nil {
			return batchFailure(result, i, "context")
		}
		locator, exists, err := loadBatchLocator(member.LocatorPath, checkpoint)
		if err != nil {
			return batchFailure(result, i, "saved_locator")
		}
		if !exists {
			if exists, err := batchFileExists(member.ReceiptPath); err != nil || exists {
				return batchFailure(result, i, "orphan_receipt")
			}
			member.SubmissionAttempted = true
			locator, err = ahemcp.SubmitBatchCandidate(ctx, intakeCommand, checkpoint.SourceID, document, checkpoint.Batch.Extractor, checkpoint.Batch.Rows[0].Result.Records)
			if err != nil {
				return batchFailure(result, i, "submission_outcome_uncertain")
			}
			member.Locator = &locator
			if err := saveBatchLocator(member.LocatorPath, checkpoint, locator); err != nil {
				return batchFailure(result, i, "locator_publication_uncertain")
			}
		}
		member.Locator = &locator
		if err := checkBatchPendingReceipt(member, checkpoint, locator, false); err != nil {
			return batchFailure(result, i, "saved_pending_receipt")
		}
		observation, err := ahemcp.ObserveBatchCandidate(ctx, queryCommand, checkpoint.SourceID, document, checkpoint.Batch.Extractor, checkpoint.Batch.Rows[0].Result.Records, locator)
		if err != nil {
			return batchFailure(result, i, "query_unverified")
		}
		member.Observation = &observation
		member.State = observation.AdmissionOutcome + "_verified"
		if observation.AdmissionOutcome == "pending" {
			if err := checkBatchPendingReceipt(member, checkpoint, locator, true); err != nil {
				return batchFailure(result, i, "pending_receipt_publication_uncertain")
			}
		}
	}
	if err := lock.Close(); err != nil {
		return batchFailure(result, len(result.Members)-1, "recovery_lock_release")
	}
	result.summarize()
	return result, nil
}

func batchFailure(result BatchResult, i int, stage string) (BatchResult, error) {
	result.Members[i].State = "failed"
	result.Members[i].FailureStage = stage
	result.summarize()
	return result, errors.New("batch recovery stopped; inspect the complete per-candidate report and resume the same index")
}

func batchFileExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func loadBatchLocator(path string, checkpoint Checkpoint) (ahemcp.CandidateLocator, bool, error) {
	exists, err := batchFileExists(path)
	if err != nil || !exists {
		return ahemcp.CandidateLocator{}, exists, err
	}
	var stored batchLocatorFile
	if err := readReviewFile(path, &stored); err != nil {
		return ahemcp.CandidateLocator{}, true, err
	}
	if stored.SchemaVersion != "detective-candidate-locator/v1" || stored.CheckpointDigest != checkpoint.Digest {
		return ahemcp.CandidateLocator{}, true, errors.New("candidate locator does not match the exact checkpoint")
	}
	if err := ahemcp.ValidateCandidateLocator(stored.Locator); err != nil {
		return ahemcp.CandidateLocator{}, true, err
	}
	return stored.Locator, true, nil
}

func saveBatchLocator(path string, checkpoint Checkpoint, locator ahemcp.CandidateLocator) error {
	if err := ahemcp.ValidateCandidateLocator(locator); err != nil {
		return err
	}
	w, err := Reserve(path)
	if err != nil {
		return err
	}
	defer w.Close()
	return writeReviewFile(w, batchLocatorFile{"detective-candidate-locator/v1", checkpoint.Digest, locator})
}

func checkBatchPendingReceipt(member *BatchMemberResult, checkpoint Checkpoint, locator ahemcp.CandidateLocator, create bool) error {
	expected := Result{SchemaVersion: "detective-pending-resume/v1", CheckpointDigest: checkpoint.Digest, State: "pending_verified", Batch: checkpoint.Batch, ReadbackVerified: true, Handoff: ahemcp.Handoff{SchemaVersion: "ahe-mcp-pending-handoff/v0", SourceSnapshotID: locator.SourceSnapshotID, ExtractionViewID: locator.ExtractionViewID, ExtractionAttemptID: locator.ExtractionAttemptID, ProposalOccurrenceID: locator.ProposalOccurrenceID, ProposalCount: 1, Status: "pending", Replayed: locator.Replayed}}
	exists, err := batchFileExists(member.ReceiptPath)
	if err != nil {
		return err
	}
	if exists {
		actual, err := loadInspectionReceipt(member.ReceiptPath, checkpoint)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			return errors.New("saved pending receipt differs from the candidate locator")
		}
		member.PendingReceiptAvailable = true
		return nil
	}
	if !create {
		return nil
	}
	w, err := Reserve(member.ReceiptPath)
	if err != nil {
		return err
	}
	defer w.Close()
	if err := writeReviewFile(w, expected); err != nil {
		return err
	}
	member.PendingReceiptAvailable = true
	return nil
}

// InspectBatch is read-only. Without a Query launcher it reports local file
// availability, never current lifecycle. It never creates missing checkpoints.
func InspectBatch(ctx context.Context, path, queryCommand string) (BatchResult, error) {
	if ctx == nil {
		return BatchResult{}, errors.New("batch inspection requires a context")
	}
	index, err := LoadBatchIndex(path)
	if err != nil {
		return BatchResult{}, err
	}
	document, err := index.Document()
	if err != nil {
		return BatchResult{}, err
	}
	result := newBatchResult(path, index)
	result.State = "inspection"
	result.AuthorityEffect = "none"
	for i := range result.Members {
		member := &result.Members[i]
		member.State = "not_checked"
		checkpoint, err := batchMemberCheckpoint(index, document, member.Ordinal)
		if err != nil {
			return batchFailure(result, i, "checkpoint_binding")
		}
		if exists, err := batchFileExists(member.CheckpointPath); err != nil {
			return batchFailure(result, i, "checkpoint_path")
		} else if exists {
			actual, err := Load(member.CheckpointPath)
			if err != nil || !reflect.DeepEqual(actual, checkpoint) {
				return batchFailure(result, i, "checkpoint_binding")
			}
			member.CheckpointAvailable = true
		}
		locator, exists, err := loadBatchLocator(member.LocatorPath, checkpoint)
		if err != nil {
			return batchFailure(result, i, "saved_locator")
		}
		if !exists {
			if receiptExists, err := batchFileExists(member.ReceiptPath); err != nil || receiptExists {
				return batchFailure(result, i, "orphan_receipt")
			}
			continue
		}
		member.Locator = &locator
		if err := checkBatchPendingReceipt(member, checkpoint, locator, false); err != nil {
			return batchFailure(result, i, "saved_pending_receipt")
		}
		if queryCommand == "" {
			continue
		}
		observation, err := ahemcp.ObserveBatchCandidate(ctx, queryCommand, checkpoint.SourceID, document, checkpoint.Batch.Extractor, checkpoint.Batch.Rows[0].Result.Records, locator)
		if err != nil {
			return batchFailure(result, i, "query_unverified")
		}
		member.Observation = &observation
		member.State = observation.AdmissionOutcome + "_verified"
	}
	result.summarize()
	return result, nil
}
