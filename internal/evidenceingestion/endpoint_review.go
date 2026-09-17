package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ReviewedEndpointAdmissionV1 = "reviewed-endpoint-admission/v1"
const EndpointReviewV1 = "endpoint-review/v1"
const endpointReviewMaxBytes = 1 << 20

// EndpointDerivation is an explicit AND composition, not a model correctness claim.
type EndpointDerivation struct {
	ParentNodeIDs []string `json:"parent_node_ids"`
	Method        string   `json:"method"`
	Producer      string   `json:"producer"`
	TraceRef      string   `json:"trace_ref"`
}
type EndpointReviewRequest struct {
	Kind                 string              `json:"kind"`
	ProposalOccurrenceID string              `json:"proposal_occurrence_id"`
	Derivation           *EndpointDerivation `json:"derivation,omitempty"`
}
type EndpointCodeFile struct {
	FileSnapshot SourceFileSnapshot `json:"file_snapshot"`
	Text         string             `json:"text"`
}

// EndpointReview retains the immutable pre-admission display and its subject.
// Lifecycle reports current DB state separately; it is not part of that subject.
// Neither field proves a human saw the display.
type EndpointReview struct {
	Lifecycle EndpointReviewLifecycle `json:"lifecycle"`
	Subject   string                  `json:"subject"`
	Display   EndpointReviewDisplay   `json:"display"`
}

// EndpointReviewLifecycle distinguishes a new admission from receipt-bound replay.
type EndpointReviewLifecycle struct {
	Mode             string                    `json:"mode"`
	AdmissionOutcome string                    `json:"admission_outcome"`
	CanonicalRef     string                    `json:"canonical_ref,omitempty"`
	Receipt          *EndpointAdmissionReceipt `json:"receipt,omitempty"`
}
type EndpointReviewDisplay struct {
	ContractVersion string                               `json:"contract_version"`
	Request         EndpointReviewRequest                `json:"request"`
	Proposal        ProposalQueryResult                  `json:"proposal"`
	SourceBasis     *ReviewableSourceClaimReviewSnapshot `json:"source_basis,omitempty"`
	CodeFile        *EndpointCodeFile                    `json:"code_file,omitempty"`
	Ancestors       []CanonicalQueryResult               `json:"ancestors"`
	SourceLeaves    []DerivedImplementsSourceLeaf        `json:"source_leaves"`
	Limitations     string                               `json:"limitations"`
}
type ReviewedEndpointAdmissionInput struct {
	RequestID       string                `json:"request_id"`
	Review          EndpointReviewRequest `json:"review"`
	ExpectedSubject string                `json:"expected_subject"`
	Decision        string                `json:"decision"`
	DecisionReason  string                `json:"decision_reason"`
	ReviewerID      string                `json:"-"`
}
type EndpointAdmissionReceipt struct {
	ContractVersion string          `json:"contract_version"`
	RequestID       string          `json:"request_id"`
	ReviewSubject   string          `json:"review_subject"`
	ReviewerID      string          `json:"reviewer_id"`
	DecisionReason  string          `json:"decision_reason"`
	Admission       AdmissionResult `json:"admission"`
}

func validateEndpointText(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
func validateEndpointRequest(req EndpointReviewRequest) error {
	if !validateEndpointText(req.ProposalOccurrenceID, 200) || !strings.HasPrefix(req.ProposalOccurrenceID, "occ:") {
		return newDomainError(ErrorInvalidInput, "an exact proposal occurrence ID is required")
	}
	switch req.Kind {
	case "repository_code":
		if req.Derivation != nil {
			return newDomainError(ErrorInvalidInput, "code endpoint cannot carry derivation")
		}
	case "derived_spec":
		d := req.Derivation
		if d == nil || len(d.ParentNodeIDs) < 1 || len(d.ParentNodeIDs) > 8 ||
			!validateEndpointText(d.Method, 4000) || !validateEndpointText(d.Producer, 200) || !validateEndpointText(d.TraceRef, 500) {
			return newDomainError(ErrorInvalidInput, "derived endpoint requires one to eight unique sorted AND parents, method, producer and trace_ref")
		}
		for i, id := range d.ParentNodeIDs {
			if !validateEndpointText(id, 200) || !strings.HasPrefix(id, "canon-node:") || (i > 0 && d.ParentNodeIDs[i-1] >= id) {
				return newDomainError(ErrorInvalidInput, "derived parents must be exact unique sorted canonical IDs")
			}
		}
	default:
		return newDomainError(ErrorUnsupportedAdmission, "endpoint kind must be derived_spec or repository_code")
	}
	return nil
}
func endpointAdmissionInput(req EndpointReviewRequest, by, reason string) AdmissionInput {
	in := AdmissionInput{ProposalOccurrenceID: req.ProposalOccurrenceID, DecisionBy: by, DecisionReason: reason}
	if d := req.Derivation; d != nil {
		in.Derivation = &DerivationAdmissionInput{ParentNodeIDs: d.ParentNodeIDs, Method: d.Method, Producer: d.Producer, TraceRef: d.TraceRef}
	}
	return in
}
func LoadEndpointReview(ctx context.Context, pool *pgxpool.Pool, req EndpointReviewRequest) (EndpointReview, error) {
	if pool == nil {
		return EndpointReview{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if err := validateEndpointRequest(req); err != nil {
		return EndpointReview{}, err
	}
	var out EndpointReview
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		out, err = loadEndpointReview(ctx, tx, req)
		return err
	})
	return out, err
}
func loadEndpointReview(ctx context.Context, tx sqlTx, req EndpointReviewRequest) (EndpointReview, error) {
	// Bound persisted JSON before the general-purpose native loader allocates it.
	var size int
	if err := tx.queryRow(ctx, `SELECT octet_length(statement_text)+octet_length(source_refs::text)+octet_length(proposed_payload::text)
 FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, req.ProposalOccurrenceID).Scan(&size); err != nil {
		return EndpointReview{}, err
	}
	if size > 128<<10 {
		return EndpointReview{}, newDomainError(ErrorInvalidInput, "endpoint proposal exceeds review bound")
	}
	p, err := getProposalByOccurrenceID(ctx, tx, req.ProposalOccurrenceID)
	if err != nil {
		return EndpointReview{}, err
	}
	if p.ExtractionAttemptStatus != attemptStatusSucceeded || p.ProposalKind != ProposalKindStatement ||
		(p.AdmissionOutcome != admissionOutcomePending && p.AdmissionOutcome != admissionOutcomeAdmitted) {
		return EndpointReview{}, newDomainError(ErrorAdmissionStateConflict, "endpoint requires a successful pending proposal or its exact admitted replay")
	}
	canonicalID := p.CanonicalRef
	persisted := p
	lifecycle := EndpointReviewLifecycle{Mode: "pending_admission", AdmissionOutcome: p.AdmissionOutcome}
	if p.AdmissionOutcome == admissionOutcomeAdmitted {
		receipt, err := loadEndpointAdmissionReceipt(ctx, tx, p.ProposalOccurrenceID)
		if err != nil {
			return EndpointReview{}, err
		}
		if receipt == nil {
			return EndpointReview{}, newDomainError(ErrorAdmissionStateConflict, "proposal is already admitted without an endpoint review receipt")
		}
		lifecycle = EndpointReviewLifecycle{Mode: "exact_replay_only", AdmissionOutcome: p.AdmissionOutcome,
			CanonicalRef: canonicalID, Receipt: receipt}
	}
	// Keep the pre-admission display stable on exact replay, never the resulting canonical node.
	p.AdmissionOutcome = admissionOutcomePending
	p.CanonicalRef = ""
	display := EndpointReviewDisplay{ContractVersion: EndpointReviewV1, Request: req, Proposal: p,
		Ancestors: []CanonicalQueryResult{}, SourceLeaves: []DerivedImplementsSourceLeaf{},
		Limitations: "Exact bytes and native identities are checked, not semantic entailment, implementation behavior, coverage or currentness. Source-basis proposed effects are provenance context only; this approval applies solely to the endpoint request. Relation approval is separate."}
	if req.Kind == "repository_code" {
		if err := validateDerivedImplementsCode(ctx, tx, p); err != nil {
			return EndpointReview{}, err
		}
		ref := p.SourceRefs[0]
		var file RepositoryExtractorFile
		// The preceding native verifier checked the complete immutable file, coordinates and parser fact.
		if err := tx.queryRow(ctx, `SELECT f.file_snapshot_id,f.repository_snapshot_id,f.repo_id,f.commit_sha,f.path,f.blob_hash,f.git_blob_oid,f.byte_length,b.raw_content
   FROM source_file_snapshots f JOIN source_blobs b ON b.raw_content_hash=f.blob_hash
   WHERE f.file_snapshot_id=$1 AND octet_length(b.raw_content)<=$2`, ref.FileSnapshotID, 512<<10).
			Scan(&file.FileSnapshot.ID, &file.FileSnapshot.RepositorySnapshotID, &file.FileSnapshot.RepoID, &file.FileSnapshot.CommitSHA,
				&file.FileSnapshot.Path, &file.FileSnapshot.BlobHash, &file.FileSnapshot.GitBlobOID, &file.FileSnapshot.ByteLength, &file.Content); err != nil {
			return EndpointReview{}, err
		}
		display.CodeFile = &EndpointCodeFile{FileSnapshot: file.FileSnapshot, Text: string(file.Content)}
	} else {
		if p.SourceBindingKind != ProposalSourceBindingSourceSnapshot || p.CodeFact != nil || p.CodeRelation != nil {
			return EndpointReview{}, newDomainError(ErrorUnsupportedAdmission, "derived specification requires an original source-backed statement proposal")
		}
		// Reconstruct the exact intake manifest and source excerpts, not a caller-authored source card.
		if err := endpointSourcePreflight(ctx, tx, p.ExtractionAttemptID); err != nil {
			return EndpointReview{}, err
		}
		basis, err := loadSourceClaimReviewSnapshotForLifecycleInTx(ctx, tx, p.ExtractionAttemptID, p.ProposalOccurrenceID, canonicalID)
		if err != nil {
			return EndpointReview{}, err
		}
		display.SourceBasis = &basis
		if err := loadEndpointAncestors(ctx, tx, req.Derivation.ParentNodeIDs, &display); err != nil {
			return EndpointReview{}, err
		}
	}
	payload, err := deterministicJSON(display)
	if err != nil {
		return EndpointReview{}, err
	}
	if len(payload) > endpointReviewMaxBytes {
		return EndpointReview{}, newDomainError(ErrorInvalidInput, "complete endpoint review exceeds one MiB; use a smaller source/proposal scope")
	}
	subject := "endpoint-review:sha256:" + hashHex(payload)
	if receipt := lifecycle.Receipt; receipt != nil {
		if receipt.ReviewSubject != subject {
			return EndpointReview{}, newDomainError(ErrorReviewContractConflict, "admitted endpoint review differs; only the original exact replay is available")
		}
		native := endpointAdmissionInput(req, receipt.ReviewerID, receipt.DecisionReason)
		if err := validateEndpointBinding(ctx, tx, persisted, native); err != nil {
			return EndpointReview{}, err
		}
		if err := validatePersistedOrdinaryAdmissionMutation(ctx, tx, persisted, native); err != nil {
			return EndpointReview{}, err
		}
	}
	return EndpointReview{Lifecycle: lifecycle, Subject: subject, Display: display}, nil
}
func endpointSourcePreflight(ctx context.Context, tx sqlTx, attempt string) error {
	var size, spans, proposals int
	err := tx.queryRow(ctx, `SELECT octet_length(b.raw_content)+octet_length(v.rendered_content)+octet_length(a.fixture_output::text),
 (SELECT count(*) FROM span_catalog_entries WHERE extraction_view_id=v.extraction_view_id),
 (SELECT count(*) FROM proposal_occurrences WHERE extraction_attempt_id=a.extraction_attempt_id)
 FROM extraction_attempts a JOIN extraction_runs r USING(extraction_run_id)
 JOIN source_snapshots s ON s.source_snapshot_id=r.source_snapshot_id JOIN source_blobs b USING(raw_content_hash)
 JOIN extraction_views v ON v.extraction_view_id=r.extraction_view_id WHERE a.extraction_attempt_id=$1`, attempt).Scan(&size, &spans, &proposals)
	if err != nil {
		return err
	}
	if size > 512<<10 || spans > 4096 || proposals > 205 {
		return newDomainError(ErrorInvalidInput, "endpoint source exceeds bounded reconstruction")
	}
	return nil
}
func loadEndpointAncestors(ctx context.Context, tx sqlTx, parents []string, display *EndpointReviewDisplay) error {
	// A virtual new root applies the same complete AND/depth/size limits as implements.
	const proposedRoot = "endpoint:proposed-root"
	headers, err := recursiveImplementsClosure(ctx, proposedRoot, func(ctx context.Context, id string) (recursiveImplementsHeader, error) {
		if id == proposedRoot {
			return recursiveImplementsHeader{kind: evidencegraph.CanonicalDerivedClaim, parents: parents}, nil
		}
		return loadRecursiveImplementsHeader(ctx, tx, id)
	})
	if err != nil {
		return err
	}
	delete(headers, proposedRoot)
	ids := make([]string, 0, len(headers))
	for id := range headers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	total := 0
	for _, id := range ids {
		node, _, err := loadDerivedImplementsAdmittedNode(ctx, tx, id, headers[id].kind)
		if err != nil {
			return err
		}
		encoded, err := deterministicJSON(node)
		if err != nil {
			return err
		}
		total += len(encoded)
		if total > endpointReviewMaxBytes {
			return newDomainError(ErrorInvalidInput, "endpoint ancestor display exceeds bound")
		}
		display.Ancestors = append(display.Ancestors, node)
		if node.NodeKind == evidencegraph.CanonicalSourceClaim {
			if err := endpointSourcePreflight(ctx, tx, node.OriginProposal.ExtractionAttemptID); err != nil {
				return err
			}
			leaf, err := loadDerivedImplementsSourceLeaf(ctx, tx, node)
			if err != nil {
				return err
			}
			encoded, err := deterministicJSON(leaf)
			if err != nil {
				return err
			}
			total += len(encoded)
			if total > endpointReviewMaxBytes {
				return newDomainError(ErrorInvalidInput, "endpoint source display exceeds bound")
			}
			display.SourceLeaves = append(display.SourceLeaves, leaf)
		}
	}
	return nil
}
func AdmitReviewedEndpoint(ctx context.Context, pool *pgxpool.Pool, input ReviewedEndpointAdmissionInput) (EndpointAdmissionReceipt, error) {
	if pool == nil {
		return EndpointAdmissionReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if err := validateEndpointRequest(input.Review); err != nil {
		return EndpointAdmissionReceipt{}, err
	}
	if input.Decision != "approved" || !validateEndpointText(input.RequestID, 200) || !validateEndpointText(input.ReviewerID, 200) ||
		!validateEndpointText(input.DecisionReason, 2000) || !validateEndpointText(input.ExpectedSubject, 200) {
		return EndpointAdmissionReceipt{}, newDomainError(ErrorInvalidInput, "explicit endpoint approval, exact subject, request ID, reviewer and reason are required")
	}
	var result EndpointAdmissionReceipt
	err := runReviewedSourceClaimAdmissionAttempts(func() error {
		return withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
				return err
			}
			p, err := loadProposalForAdmission(ctx, tx, input.Review.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			review, err := loadEndpointReview(ctx, tx, input.Review)
			if err != nil {
				return err
			}
			if review.Subject != input.ExpectedSubject {
				return newDomainError(ErrorReviewContractConflict, "endpoint review subject changed")
			}
			native := endpointAdmissionInput(input.Review, input.ReviewerID, input.DecisionReason)
			if p.AdmissionOutcome == admissionOutcomeAdmitted {
				receipt, err := loadEndpointAdmissionReceipt(ctx, tx, p.ProposalOccurrenceID)
				if err != nil {
					return err
				}
				if receipt == nil || receipt.RequestID != input.RequestID || receipt.ReviewerID != input.ReviewerID ||
					receipt.DecisionReason != input.DecisionReason || receipt.ReviewSubject != input.ExpectedSubject {
					return newDomainError(ErrorAdmissionStateConflict, "endpoint replay differs from original review")
				}
				if err := validateEndpointBinding(ctx, tx, p, native); err != nil {
					return err
				}
				if err := validatePersistedOrdinaryAdmissionMutation(ctx, tx, p, native); err != nil {
					return err
				}
				result = *receipt
				result.Admission.Replayed = true
				return nil
			}
			mutation, err := buildCanonicalAdmissionMutation(p, native)
			if err != nil {
				return err
			}
			if mutation.derivation != nil {
				if err := validateDerivedAdmissionWithLock(ctx, tx, mutation, true); err != nil {
					return err
				}
			}
			write, err := persistOrdinaryCanonicalMutation(ctx, tx, mutation)
			if err != nil {
				return err
			}
			if mutation.derivation != nil {
				if err := insertCanonicalDerivation(ctx, tx, *mutation.derivation, mutation.derivationParentEdges, p.ProposalOccurrenceID); err != nil {
					return err
				}
			}
			if err := insertAdmissionDecision(ctx, tx, mutation.decision, admissionDecisionMetadata{
				DecisionBy: input.ReviewerID, DecisionReason: input.DecisionReason, ReviewBindingContractVersion: ReviewedEndpointAdmissionV1}); err != nil {
				return err
			}
			if err := insertOrdinaryAdmissionAuthority(ctx, tx, mutation, write); err != nil {
				return err
			}
			if err := markProposalAdmitted(ctx, tx, p.ProposalOccurrenceID, mutation.result.CanonicalRef); err != nil {
				return err
			}
			result = EndpointAdmissionReceipt{ContractVersion: ReviewedEndpointAdmissionV1, RequestID: input.RequestID, ReviewSubject: review.Subject,
				ReviewerID: input.ReviewerID, DecisionReason: input.DecisionReason, Admission: mutation.result}
			payload, err := deterministicJSON(review.Display)
			if err != nil {
				return err
			}
			receipt, err := deterministicJSON(result)
			if err != nil {
				return err
			}
			_, err = tx.exec(ctx, `INSERT INTO canonical_endpoint_review_bindings
    (admission_decision_id,proposal_occurrence_id,request_id,review_subject,display_payload,receipt_payload)
    VALUES ($1,$2,$3,$4,$5,$6::jsonb)`, mutation.result.AdmissionDecisionID, p.ProposalOccurrenceID, input.RequestID, review.Subject, string(payload), string(receipt))
			return err
		})
	})
	if err != nil {
		return EndpointAdmissionReceipt{}, err
	}
	return result, nil
}
func loadEndpointAdmissionReceipt(ctx context.Context, db sqlQueryer, proposalID string) (*EndpointAdmissionReceipt, error) {
	var raw []byte
	// COALESCE makes absence explicit; legacy source/derived origins remain readable.
	err := db.queryRow(ctx, `SELECT (SELECT receipt_payload FROM canonical_endpoint_review_bindings WHERE proposal_occurrence_id=$1)`, proposalID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var receipt EndpointAdmissionReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}
func validateEndpointBinding(ctx context.Context, tx sqlTx, p ProposalQueryResult, input AdmissionInput) error {
	_, meta, err := loadAdmissionDecisionResult(ctx, tx, p.ProposalOccurrenceID)
	if err != nil {
		return err
	}
	receipt, err := loadEndpointAdmissionReceipt(ctx, tx, p.ProposalOccurrenceID)
	if err != nil {
		return err
	}
	if meta.ReviewBindingContractVersion != ReviewedEndpointAdmissionV1 {
		if receipt != nil {
			return newDomainError(ErrorCanonicalAdmissionInvariant, "endpoint binding has a different admission origin")
		}
		return nil
	}
	if receipt == nil || receipt.ContractVersion != ReviewedEndpointAdmissionV1 || receipt.ReviewerID != meta.DecisionBy || receipt.DecisionReason != meta.DecisionReason {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "endpoint reviewed origin is missing or differs")
	}
	var subject, payload string
	if err := tx.queryRow(ctx, `SELECT review_subject,display_payload FROM canonical_endpoint_review_bindings WHERE proposal_occurrence_id=$1`, p.ProposalOccurrenceID).Scan(&subject, &payload); err != nil {
		return err
	}
	if subject != "endpoint-review:sha256:"+hashHex([]byte(payload)) || subject != receipt.ReviewSubject {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "endpoint review digest differs")
	}
	var display EndpointReviewDisplay
	if err := json.Unmarshal([]byte(payload), &display); err != nil {
		return err
	}
	expected := endpointAdmissionInput(display.Request, input.DecisionBy, input.DecisionReason)
	if display.ContractVersion != EndpointReviewV1 || !reflect.DeepEqual(expected, input) {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "endpoint review request differs from native admission")
	}
	if receipt.Admission.CanonicalRef != p.CanonicalRef || receipt.Admission.ProposalOccurrenceID != p.ProposalOccurrenceID {
		return fmt.Errorf("endpoint receipt differs from canonical proposal")
	}
	return nil
}
