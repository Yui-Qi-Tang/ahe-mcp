//go:build integration

package evidenceingestion

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
)

// TEST APPROVAL STUB: even a bounded writer cannot bypass the native withdrawal
// guard by forging a structurally valid request with the newest cut revision.
func TestIntegrationInitialRegistrationSQLRejectsWithdrawnClaim(t *testing.T) {
	ctx, owner := initialRegistrationRoleDatabase(t)
	if _, err := migrations.ApplyUp(ctx, owner); err != nil {
		t.Fatal(err)
	}
	in := registrationNativeFixture(t, ctx, owner, "sql-withdrawal", "AHE-42")
	review, err := LoadInitialLineageRegistrationReview(ctx, owner, in.Review)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := initialRegistrationBoundedPool(t, ctx, owner, dbrole.ProfileInitialLineageReviewer)
	d := governanceTestDecision(t, ctx, owner, GovernanceRequest{Kind: "withdrawal", TargetID: in.Review.ClaimID, Operation: "withdraw"}, "sql-withdrawal")
	if _, err := ApplyGovernanceDecision(ctx, owner, d); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT revision FROM evidence_governance_head`).Scan(&review.Grounding.GovernanceRevision); err != nil {
		t.Fatal(err)
	}
	grounding, err := json.Marshal(review.Grounding)
	if err != nil {
		t.Fatal(err)
	}
	review.Binding.GroundingHash = governanceDigest("sha256:", grounding)
	subject, err := review.Binding.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	p := evidencesupersession.InitialRegistrationPayload{ContractVersion: evidencesupersession.InitialRegistrationContractV1, Binding: review.Binding, ReviewSubjectID: subject, RequestID: in.RequestID, Reviewer: in.ReviewerID, Reason: in.Reason}
	id, err := p.ID()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := json.Marshal(struct {
		Contract string                                         `json:"contract_version"`
		Binding  evidencesupersession.RegistrationReviewBinding `json:"binding"`
	}{p.ContractVersion, p.Binding})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reviewer.Exec(ctx, `INSERT INTO canonical_initial_lineage_registrations(event_id,request_id,claim_id,lineage_key,body,binding_body,grounding_body) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, in.RequestID, in.Review.ClaimID, p.Binding.LineageKey, string(body), string(binding), string(grounding))
	if err == nil || !strings.Contains(err.Error(), "withdrawn claim cannot register") {
		t.Fatal("bounded SQL bypassed withdrawal guard", err)
	}
	var count int
	if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_initial_lineage_registrations)+(SELECT count(*) FROM canonical_supersession_members)+(SELECT count(*) FROM canonical_supersession_lineages)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed SQL left membership authority", count, err)
	}
}
