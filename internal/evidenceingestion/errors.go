// Package evidenceingestion implements source-bound evidence ingestion.
package evidenceingestion

import (
	"errors"
	"fmt"
)

// ErrorKind identifies a typed ingestion domain failure.
type ErrorKind string

const (
	// ErrorInvalidUTF8 means source bytes were not valid UTF-8.
	ErrorInvalidUTF8 ErrorKind = "invalid_utf8"
	// ErrorUnknownSpan means a proposal referenced a span outside the catalog.
	ErrorUnknownSpan ErrorKind = "unknown_span"
	// ErrorSpanOutOfBounds means a span points outside rendered view bytes.
	ErrorSpanOutOfBounds ErrorKind = "span_out_of_bounds"
	// ErrorQuotedHashMismatch means catalog quote bytes no longer match their hash.
	ErrorQuotedHashMismatch ErrorKind = "quoted_hash_mismatch"
	// ErrorDuplicateProposalLocalID means a batch reused a local proposal ID.
	ErrorDuplicateProposalLocalID ErrorKind = "duplicate_proposal_local_id"
	// ErrorMissingSourceViewAttempt means required persisted provenance is absent.
	ErrorMissingSourceViewAttempt ErrorKind = "missing_source_view_attempt"
	// ErrorSourceStateNotFound means an exact immutable source state is absent.
	ErrorSourceStateNotFound ErrorKind = "source_state_not_found"
	// ErrorOccurrenceConflict means an occurrence ID maps to different content.
	ErrorOccurrenceConflict ErrorKind = "occurrence_conflict"
	// ErrorInvalidInput means required input fields are absent or malformed.
	ErrorInvalidInput ErrorKind = "invalid_input"
	// ErrorInvalidRecordID means a record ID has the wrong stable prefix or shape.
	ErrorInvalidRecordID ErrorKind = "invalid_record_id"
	// ErrorPersistedAttemptFailed means replay found a previously failed attempt.
	ErrorPersistedAttemptFailed ErrorKind = "persisted_attempt_failed"
	// ErrorIdempotencyKeyReused means a request ID was reused with different source payload.
	ErrorIdempotencyKeyReused ErrorKind = "idempotency_key_reused"
	// ErrorRepositoryRevisionMismatch means a requested commit is not the verified canonical revision.
	ErrorRepositoryRevisionMismatch ErrorKind = "repository_revision_mismatch"
	// ErrorRepositorySnapshotIntegrity means persisted repository authority is internally inconsistent.
	ErrorRepositorySnapshotIntegrity ErrorKind = "repository_snapshot_integrity"
	// ErrorRunnerInvocationFailed means the trusted local extractor runner returned an invocation error.
	ErrorRunnerInvocationFailed ErrorKind = "runner_invocation_failed"
	// ErrorRunnerInvocationTimeout means the trusted local extractor runner timed out.
	ErrorRunnerInvocationTimeout ErrorKind = "runner_invocation_timeout"
	// ErrorRunnerInvocationCancelled means the trusted local extractor runner was cancelled.
	ErrorRunnerInvocationCancelled ErrorKind = "runner_invocation_cancelled"
	// ErrorInvalidExtractorOutput means the trusted local extractor returned malformed or forbidden output.
	ErrorInvalidExtractorOutput ErrorKind = "invalid_extractor_output"
	// ErrorUnsupportedAdmission means a proposal cannot enter the Slice 9 canonical admission path.
	ErrorUnsupportedAdmission ErrorKind = "unsupported_admission"
	// ErrorAdmissionStateConflict means a proposal is not in a legal state for the requested admission mutation.
	ErrorAdmissionStateConflict ErrorKind = "admission_state_conflict"
	// ErrorDerivationInvariant means a derived admission would violate canonical derivation invariants.
	ErrorDerivationInvariant ErrorKind = "derivation_invariant"
	// ErrorSourceGenerationConflict means a source generation or head transition violates lifecycle invariants.
	ErrorSourceGenerationConflict ErrorKind = "source_generation_conflict"
	// ErrorChangeObservationConflict means a persisted change token maps to inconsistent observation material.
	ErrorChangeObservationConflict ErrorKind = "change_observation_conflict"
	// ErrorRepositoryWorkConflict means repository extraction work violates scheduling or claim invariants.
	ErrorRepositoryWorkConflict ErrorKind = "repository_work_conflict"
	// ErrorRepositoryWorkLeaseExpired means a worker tried to mutate work after its claim lease ended.
	ErrorRepositoryWorkLeaseExpired ErrorKind = "repository_work_lease_expired"
	// ErrorRepositoryWorkExecutionFailed means an unclassified execution failure was durably replayed.
	ErrorRepositoryWorkExecutionFailed ErrorKind = "repository_work_execution_failed"
)

// DomainError carries a stable error kind for persistence and tests.
type DomainError struct {
	Kind    ErrorKind
	Message string
}

func (e *DomainError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Kind)
	}
	return string(e.Kind) + ": " + e.Message
}

func newDomainError(kind ErrorKind, format string, args ...any) error {
	return &DomainError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// KindOf returns the stable ingestion error kind when err wraps a DomainError.
func KindOf(err error) (ErrorKind, bool) {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Kind, true
	}
	return "", false
}
