package mcpadmin

import (
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

// Only fixed review coordinates are accepted. The native atomic writer owns
// receipt construction and replay; this transport has no new request/session
// identity or caller-issued approval token.
func getSourceClaimReviewSchema() map[string]any {
	return objectSchema(map[string]any{
		"extraction_attempt_id":  sourceReviewIDSchema("attempt:", "Exact persisted extraction attempt ID."),
		"proposal_occurrence_id": sourceReviewIDSchema("occ:", "One pending source-backed statement in the complete batch manifest."),
	}, []string{"extraction_attempt_id", "proposal_occurrence_id"})
}

func admitReviewedSourceClaimSchema() map[string]any {
	reason := boundedStringSchema("Explicit approved reason, already trimmed; 1 to 2000 UTF-8 bytes. The launcher supplies reviewer identity. Replay requires the identical subject, reviewer and reason.", evidenceingestion.ProposalDispositionDecisionReasonMaxBytes)
	reason["minLength"] = 1
	return objectSchema(map[string]any{
		"extraction_attempt_id": sourceReviewIDSchema("attempt:", "Exact persisted extraction attempt from get_source_claim_review."),
		"expected_subject": objectSchema(map[string]any{
			"review_subject": objectSchema(map[string]any{
				"submission_receipt_id":  sourceReviewIDSchema("submission-receipt:v1:sha256:", "Unchanged submission receipt identity from the review response."),
				"proposal_manifest_id":   sourceReviewIDSchema("proposal-manifest:v1:sha256:", "Unchanged complete manifest identity from the review response."),
				"proposal_occurrence_id": sourceReviewIDSchema("occ:", "Exact selected proposal; this does not approve other batch members."),
				"proposal_basis_id":      sourceReviewIDSchema("proposal-basis:v1:sha256:", "Exact proposal basis identity from the review response."),
				"review_package_id":      sourceReviewIDSchema("review-package:v1:sha256:", "Exact package identity from the review response."),
			}, []string{"submission_receipt_id", "proposal_manifest_id", "proposal_occurrence_id", "proposal_basis_id", "review_package_id"}),
			"review_display_artifact_id": sourceReviewIDSchema("review-display:v1:sha256:", "Unchanged exact display-byte identity; the writer reconstructs and compares this atomically."),
		}, []string{"review_subject", "review_display_artifact_id"}),
		"decision":        enumStringSchema("Only explicit approval invokes this admission writer; rejection and audit-only use the separate reviewed disposition writer.", "approved"),
		"decision_reason": reason,
	}, []string{"extraction_attempt_id", "expected_subject", "decision", "decision_reason"})
}

func recordReviewedSourceClaimDispositionSchema() map[string]any {
	schema := admitReviewedSourceClaimSchema()
	properties := schema["properties"].(map[string]any)
	properties["decision"] = enumStringSchema("Explicit noncanonical outcome after reviewing the complete unchanged display.", "reject", "audit_only")
	properties["decision_reason"] = boundedStringSchema("Explicit nonempty already-trimmed reason; identical subject, outcome, reviewer and reason are required for replay.", evidenceingestion.ProposalDispositionDecisionReasonMaxBytes)
	properties["decision_reason"].(map[string]any)["minLength"] = 1
	return schema
}

func sourceReviewIDSchema(prefix, description string) map[string]any {
	return map[string]any{
		"type": "string", "description": description,
		"pattern":   "^" + prefix + "[0-9a-f]{64}$",
		"minLength": len(prefix) + 64, "maxLength": len(prefix) + 64,
	}
}

// SourceClaimReviewerPrincipal exposes the constructor-bound identity only.
func (b *Backend) SourceClaimReviewerPrincipal() runtimeauth.Principal {
	if b == nil {
		return runtimeauth.Principal{}
	}
	return b.reviewPrincipal
}

func ingestionTools() []mcpstdio.Tool {
	readOnly, write, terminal, no, idempotent := true, false, true, false, true
	return append(legacyIngestionTools(),
		mcpstdio.Tool{Name: evidenceingestionmcp.ToolGetSourceClaimReview, Title: "Get Source Claim Review", Description: evidenceingestionmcp.GetSourceClaimReviewDescription,
			InputSchema: getSourceClaimReviewSchema(), Annotations: mcpstdio.Annotations{ReadOnlyHint: &readOnly, DestructiveHint: &no, IdempotentHint: &idempotent}},
		mcpstdio.Tool{Name: evidenceingestionmcp.ToolAdmitReviewedSourceClaim, Title: "Admit Reviewed Source Claim", Description: evidenceingestionmcp.AdmitReviewedSourceClaimDescription,
			InputSchema: admitReviewedSourceClaimSchema(), Annotations: mcpstdio.Annotations{ReadOnlyHint: &write, DestructiveHint: &terminal, IdempotentHint: &idempotent}},
		mcpstdio.Tool{Name: evidenceingestionmcp.ToolRecordReviewedSourceClaimDisposition, Title: "Record Reviewed Source Claim Disposition", Description: evidenceingestionmcp.RecordReviewedSourceClaimDispositionDescription,
			InputSchema: recordReviewedSourceClaimDispositionSchema(), Annotations: mcpstdio.Annotations{ReadOnlyHint: &write, DestructiveHint: &terminal, IdempotentHint: &idempotent}},
	)
}
