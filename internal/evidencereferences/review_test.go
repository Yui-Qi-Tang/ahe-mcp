package evidencereferences

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestReferencesExactReviewAndIndependentReceipt(t *testing.T) {
	req, basis := resolverFixture(t, ResolverSameSnapshotV1, true, "")
	review, err := buildReview(req, basis)
	if err != nil {
		t.Fatal(err)
	}
	again, err := buildReview(req, basis)
	if err != nil || !reflect.DeepEqual(review, again) {
		t.Fatalf("deterministic review: %v", err)
	}
	var body ReviewBody
	if err = json.Unmarshal([]byte(review.Display.PayloadUTF8), &body); err != nil {
		t.Fatal(err)
	}
	if body.From.Claim != basis.From.ReviewSnapshot.ReviewPackage.ProposalBasis.StatementText ||
		body.To.SourceSnapshotID != req.Target.SourceSnapshotID || len(body.Limitations) != 3 || body.Effect != "direct_structural_navigation_only" {
		t.Fatalf("incomplete review: %+v", body)
	}
	in := AdmissionInput{RequestID: "reviewed-ref", Review: req, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "synthetic-reviewer", DecisionReason: "synthetic testing", ProducerSessionRef: "session-a"}
	a, edge, receipt, err := admissionRecord(in, review, basis.From.Node.OriginProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != a.ReceiptID || edge.Provenance.ReviewRef != receipt.ID || edge.Provenance.Method != "reviewed_references" {
		t.Fatal("independent receipt lost")
	}
	in.ProducerSessionRef = "session-b"
	b, otherEdge, otherReceipt, err := admissionRecord(in, review, basis.From.Node.OriginProposalOccurrenceID)
	if err != nil || receipt != otherReceipt || !reflect.DeepEqual(edge, otherEdge) || a == b {
		t.Fatalf("session entered identity or vanished from audit: %v", err)
	}
}

func TestReferencesRetainedAuthorityRejectsDrift(t *testing.T) {
	req, basis := resolverFixture(t, ResolverSameSnapshotV1, true, "")
	review, err := buildReview(req, basis)
	if err != nil {
		t.Fatal(err)
	}
	in := AdmissionInput{RequestID: "reviewed-ref", Review: req, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "reviewer", DecisionReason: "test"}
	a, edge, _, err := admissionRecord(in, review, basis.From.Node.OriginProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*evidenceingestion.CanonicalReferencesAdmissionAuthority){
		"reviewer": func(a *evidenceingestion.CanonicalReferencesAdmissionAuthority) { a.ReviewerID = "different" },
		"reason":   func(a *evidenceingestion.CanonicalReferencesAdmissionAuthority) { a.DecisionReason = "different" },
		"request":  func(a *evidenceingestion.CanonicalReferencesAdmissionAuthority) { a.RequestID = "different" },
		"cut":      func(a *evidenceingestion.CanonicalReferencesAdmissionAuthority) { a.CutID += "different" },
		"unknown receipt field": func(a *evidenceingestion.CanonicalReferencesAdmissionAuthority) {
			a.ReceiptPayloadUTF8 = strings.TrimSuffix(a.ReceiptPayloadUTF8, "}") + `,"extra":true}`
			a.ReceiptPayloadHash = evidenceingestion.ReferencesPayloadHash(a.ReceiptPayloadUTF8)
		},
		"display exact bytes":  func(a *evidenceingestion.CanonicalReferencesAdmissionAuthority) { a.ReviewPayloadUTF8 += " " },
		"unsupported contract": func(a *evidenceingestion.CanonicalReferencesAdmissionAuthority) { a.ContractVersion = "v99" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := a
			mutate(&bad)
			if err := evidenceingestion.ValidateCanonicalReferencesReadAuthority(edge, bad); err == nil {
				t.Fatal("accepted drift")
			}
		})
	}
}

func TestReferencesAdmissionEnvelopeAndFraming(t *testing.T) {
	if evidenceingestion.ReferencesFramedHash("ab", "c") == evidenceingestion.ReferencesFramedHash("a", "bc") {
		t.Fatal("ambiguous framing")
	}
	in := AdmissionInput{RequestID: "r", Decision: "approved", ReviewerID: "reviewer", DecisionReason: "test", ExpectedSubject: ReviewSubject{ReviewSubjectID: "r", CutID: "c"}}
	if err := validateAdmission(in); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"", "reject", "audit_only", " approved"} {
		bad := in
		bad.Decision = decision
		if validateAdmission(bad) == nil {
			t.Fatal("accepted nonapproval")
		}
	}
	for _, text := range []string{" ", "bad\x00", strings.Repeat("r", 201)} {
		bad := in
		bad.RequestID = text
		if validateAdmission(bad) == nil {
			t.Fatal("accepted invalid request")
		}
	}
}

func TestReferencesReceiptRequiresExactApprovalSubject(t *testing.T) {
	for name, mutate := range map[string]func(*AdmissionInput){
		"not approved":     func(in *AdmissionInput) { in.Decision = "reject" },
		"missing reviewer": func(in *AdmissionInput) { in.ReviewerID = "" },
		"changed display":  func(in *AdmissionInput) { in.ExpectedSubject.ReviewSubjectID += "changed" },
		"changed cut":      func(in *AdmissionInput) { in.ExpectedSubject.CutID += "changed" },
		"changed request":  func(in *AdmissionInput) { in.Review.ToNodeID += "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			req, basis := resolverFixture(t, ResolverSameSnapshotV1, true, "")
			review, err := buildReview(req, basis)
			if err != nil {
				t.Fatal(err)
			}
			in := AdmissionInput{RequestID: "synthetic-ref", Review: req, ExpectedSubject: review.Subject,
				Decision: "approved", ReviewerID: "synthetic-reviewer", DecisionReason: "Contract test only."}
			mutate(&in)
			authority, edge, receipt, err := admissionRecord(in, review, basis.From.Node.OriginProposalOccurrenceID)
			if err == nil || !reflect.DeepEqual(authority, evidenceingestion.CanonicalReferencesAdmissionAuthority{}) ||
				edge.ID != "" || receipt.ID != "" {
				t.Fatal("unapproved or altered review produced a receipt")
			}
		})
	}
}
