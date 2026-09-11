package evidenceingestionmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

// These tests use the immutable synthetic golden and a narrow core stub. They
// prove transport fidelity/dispatch, not a database admission or human review.
type fakeSourceReviewCore struct {
	snapshot                evidenceingestion.ReviewableSourceClaimReviewSnapshot
	result                  evidenceingestion.AdmissionResult
	err                     error
	loads, writes           int
	attemptID, occurrenceID string
	input                   evidenceingestion.ReviewedSourceClaimAdmissionInput
}

func (f *fakeSourceReviewCore) LoadReviewableSourceClaimReviewSnapshot(_ context.Context, attemptID, occurrenceID string) (evidenceingestion.ReviewableSourceClaimReviewSnapshot, error) {
	f.loads++
	f.attemptID, f.occurrenceID = attemptID, occurrenceID
	return f.snapshot, f.err
}

func (f *fakeSourceReviewCore) AdmitReviewedSourceClaim(_ context.Context, input evidenceingestion.ReviewedSourceClaimAdmissionInput) (evidenceingestion.AdmissionResult, error) {
	f.writes++
	f.input = input
	return f.result, f.err
}

func sourceReviewTestServer(core *fakeSourceReviewCore) *Server {
	return &Server{sourceReview: core, reviewPrincipal: runtimeauth.Principal{ID: "reviewer:test"}}
}

func sourceReviewGolden(t *testing.T) (evidenceingestion.ReviewableSourceClaimReviewSnapshot, evidenceingestion.SourceClaimReviewDisplayArtifact, evidenceingestion.ExactDisplayedReviewSubject) {
	t.Helper()
	data, err := os.ReadFile("../evidenceingestion/testdata/review_handoff_one_row_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		WireUTF8 string `json:"wire_utf8"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Receipt  evidenceingestion.SubmissionReceipt                `json:"submission_receipt"`
		Manifest evidenceingestion.ProposalBatchManifest            `json:"proposal_manifest"`
		Display  evidenceingestion.SourceClaimReviewDisplayArtifact `json:"display_artifact"`
		Subject  evidenceingestion.ExactDisplayedReviewSubject      `json:"displayed_subject"`
	}
	if err := json.Unmarshal([]byte(fixture.WireUTF8), &wire); err != nil {
		t.Fatal(err)
	}
	var review evidenceingestion.ReviewPackage
	if err := json.Unmarshal([]byte(wire.Display.PayloadUTF8), &review); err != nil {
		t.Fatal(err)
	}
	snapshot := evidenceingestion.ReviewableSourceClaimReviewSnapshot{SubmissionReceipt: wire.Receipt, ProposalManifest: wire.Manifest, ReviewPackage: review, ExactReviewSubject: wire.Subject.ReviewSubject}
	if err := evidenceingestion.ValidateExactSourceClaimReviewDisplay(snapshot, wire.Display, wire.Subject); err != nil {
		t.Fatalf("historical golden must remain valid: %v", err)
	}
	return snapshot, wire.Display, wire.Subject
}

func sourceReviewApprovedRequest(t *testing.T) AdmitReviewedSourceClaimRequest {
	t.Helper()
	snapshot, _, subject := sourceReviewGolden(t)
	return AdmitReviewedSourceClaimRequest{ExtractionAttemptID: snapshot.SubmissionReceipt.ExtractionAttemptID, ExpectedSubject: subject, Decision: "approved", DecisionReason: "合成 transport 測試，非人工核准證明"}
}

func TestSourceClaimReviewReturnsExactCompleteGoldenWithoutWriting(t *testing.T) {
	snapshot, display, subject := sourceReviewGolden(t)
	core := &fakeSourceReviewCore{snapshot: snapshot}
	req := GetSourceClaimReviewRequest{ExtractionAttemptID: snapshot.SubmissionReceipt.ExtractionAttemptID, ProposalOccurrenceID: subject.ReviewSubject.ProposalOccurrenceID}
	payload, _ := json.Marshal(req)
	wire, err := sourceReviewTestServer(core).CallTool(t.Context(), ToolGetSourceClaimReview, payload)
	if err != nil {
		t.Fatal(err)
	}
	var got GetSourceClaimReviewResponse
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if got.ContractVersion != SourceClaimReviewMCPContract || got.Display != display || got.Subject != subject ||
		!reflect.DeepEqual(got.SubmissionReceipt, snapshot.SubmissionReceipt) || !reflect.DeepEqual(got.ProposalManifest, snapshot.ProposalManifest) ||
		core.loads != 1 || core.writes != 0 || core.attemptID != req.ExtractionAttemptID || core.occurrenceID != req.ProposalOccurrenceID {
		t.Fatalf("exact snapshot response drifted: %+v", got)
	}
	if !strings.Contains(got.Display.PayloadUTF8, `"source_limitations":[]`) || len(wire) > SourceClaimReviewMaxResponseBytes {
		t.Fatal("complete display shape or response bound lost")
	}
}

func TestSourceClaimReviewRejectsPartialOrDriftedSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*evidenceingestion.ReviewableSourceClaimReviewSnapshot)
	}{
		{"wrong_attempt", func(s *evidenceingestion.ReviewableSourceClaimReviewSnapshot) {
			s.SubmissionReceipt.ExtractionAttemptID = "attempt:" + strings.Repeat("a", 64)
		}},
		{"wrong_occurrence", func(s *evidenceingestion.ReviewableSourceClaimReviewSnapshot) {
			s.ExactReviewSubject.ProposalOccurrenceID = "occ:" + strings.Repeat("a", 64)
		}},
		{"nil_manifest", func(s *evidenceingestion.ReviewableSourceClaimReviewSnapshot) { s.ProposalManifest.Entries = nil }},
		{"partial_manifest", func(s *evidenceingestion.ReviewableSourceClaimReviewSnapshot) { s.ProposalManifest.ProposalCount++ }},
		{"over_205", func(s *evidenceingestion.ReviewableSourceClaimReviewSnapshot) {
			s.ProposalManifest.Entries = make([]evidenceingestion.ProposalBatchManifestEntry, 206)
			s.ProposalManifest.ProposalCount = 206
		}},
		{"statement_tamper", func(s *evidenceingestion.ReviewableSourceClaimReviewSnapshot) {
			s.ReviewPackage.ProposalBasis.StatementText = "changed"
		}},
		{"manifest_member_tamper", func(s *evidenceingestion.ReviewableSourceClaimReviewSnapshot) {
			s.ProposalManifest.Entries[0].ProposalLocalID = "changed"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, _, subject := sourceReviewGolden(t)
			req := GetSourceClaimReviewRequest{ExtractionAttemptID: snapshot.SubmissionReceipt.ExtractionAttemptID, ProposalOccurrenceID: subject.ReviewSubject.ProposalOccurrenceID}
			tc.mutate(&snapshot)
			core := &fakeSourceReviewCore{snapshot: snapshot}
			got, err := sourceReviewTestServer(core).GetSourceClaimReview(t.Context(), req)
			if err == nil || !reflect.DeepEqual(got, GetSourceClaimReviewResponse{}) || core.loads != 1 || core.writes != 0 {
				t.Fatalf("partial/drifted snapshot escaped: %+v %v", got, err)
			}
		})
	}
}

func TestSourceClaimReviewedAdmissionDelegatesAtomicReplayAndSnakeCase(t *testing.T) {
	req := sourceReviewApprovedRequest(t)
	core := &fakeSourceReviewCore{result: evidenceingestion.AdmissionResult{ProposalOccurrenceID: req.ExpectedSubject.ReviewSubject.ProposalOccurrenceID, AdmissionDecisionID: "decision:test", AdmissionOutcome: "admitted", CanonicalRef: "canon-node:test", Replayed: true}}
	payload, _ := json.Marshal(req)
	wire, err := sourceReviewTestServer(core).CallTool(t.Context(), ToolAdmitReviewedSourceClaim, payload)
	if err != nil {
		t.Fatal(err)
	}
	want := evidenceingestion.ReviewedSourceClaimAdmissionInput{ExtractionAttemptID: req.ExtractionAttemptID, ExpectedSubject: req.ExpectedSubject, DecisionBy: "reviewer:test", DecisionReason: req.DecisionReason}
	if core.input != want || core.loads != 0 || core.writes != 1 {
		t.Fatalf("atomic writer must own fresh checks/replay without a pending-only pre-read: %+v", core)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"proposal_occurrence_id", "admission_decision_id", "admission_outcome", "canonical_ref", "raw_evidence_node_ids", "canonical_edge_ids", "replayed"} {
		if fields[field] == nil {
			t.Fatalf("missing snake_case field %s: %s", field, wire)
		}
	}
	for _, field := range []string{"raw_evidence_node_ids", "canonical_edge_ids"} {
		if string(fields[field]) != "[]" {
			t.Fatalf("%s must be explicit []: %s", field, wire)
		}
	}
	if fields["CanonicalRef"] != nil || string(fields["replayed"]) != "true" {
		t.Fatalf("core struct leaked or replay lost: %s", wire)
	}
}

func TestSourceClaimReviewAuthorizationPrecedesDecodeAndCore(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server *Server
		want   error
	}{
		{"no_principal", &Server{}, runtimeauth.ErrUnauthenticated},
		{"invalid_principal", &Server{reviewPrincipal: runtimeauth.Principal{ID: " reviewer:test "}}, runtimeauth.ErrUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := &fakeSourceReviewCore{}
			tc.server.sourceReview = core
			if _, err := tc.server.CallTool(t.Context(), ToolAdmitReviewedSourceClaim, []byte("not JSON")); !errors.Is(err, tc.want) {
				t.Fatalf("wire authorize: %v", err)
			}
			if _, err := tc.server.AdmitReviewedSourceClaim(t.Context(), sourceReviewApprovedRequest(t)); !errors.Is(err, tc.want) {
				t.Fatalf("typed authorize: %v", err)
			}
			if core.loads != 0 || core.writes != 0 {
				t.Fatal("unauthorized core call")
			}
		})
	}
}

func TestSourceClaimReviewStrictJSONBoundary(t *testing.T) {
	for index, payload := range []string{
		`null`, `[]`, `{}`, `{} {}`, `{"reviewer_id":"spoof"}`, `{"decision_by":"spoof"}`, `{"request_id":"new"}`, `{"producer_session_ref":"new"}`, `{"receipt":{}}`, `{"graph":{}}`, `{"complete":true}`, `{"derivation":{}}`,
		`{"expected_subject":{"review_subject":{"confidence":1}}}`, `{"expected_subject":{"review_display_artifact_id":"a","review_display_artifact_id":"b"}}`,
		`{"decision":"rejected","Decision":"approved"}`, `{"decision":"rejected","decisi\u006fn":"approved"}`, `{"deci\u017fion":"approved"}`, `{"decision_reason":"` + string([]byte{0xff}) + `"}`,
		strings.Repeat(" ", maxReviewToolRequestBytes+1), `{"expected_subject":` + strings.Repeat("[", 34) + `0` + strings.Repeat("]", 34) + `}`,
	} {
		t.Run(fmt.Sprintf("invalid_%02d", index), func(t *testing.T) {
			core := &fakeSourceReviewCore{}
			_, err := sourceReviewTestServer(core).CallTool(t.Context(), ToolAdmitReviewedSourceClaim, []byte(payload))
			var toolErr *ToolError
			if !errors.As(err, &toolErr) || toolErr.Code != toolErrorInvalidRequest || core.loads != 0 || core.writes != 0 {
				t.Fatalf("ambiguous payload reached core: %v", err)
			}
		})
	}
}

func TestSourceClaimReviewRequiresExactApprovalCoordinatesAndReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*AdmitReviewedSourceClaimRequest)
	}{
		{"missing_decision", func(r *AdmitReviewedSourceClaimRequest) { r.Decision = "" }},
		{"rejected", func(r *AdmitReviewedSourceClaimRequest) { r.Decision = "rejected" }},
		{"audit_only", func(r *AdmitReviewedSourceClaimRequest) { r.Decision = "audit_only" }},
		{"missing_subject", func(r *AdmitReviewedSourceClaimRequest) {
			r.ExpectedSubject = evidenceingestion.ExactDisplayedReviewSubject{}
		}},
		{"uppercase_hash", func(r *AdmitReviewedSourceClaimRequest) { r.ExtractionAttemptID = "attempt:" + strings.Repeat("A", 64) }},
		{"bad_prefix", func(r *AdmitReviewedSourceClaimRequest) {
			r.ExpectedSubject.ReviewSubject.ProposalBasisID = "receipt:v1:sha256:" + strings.Repeat("a", 64)
		}},
		{"missing_display", func(r *AdmitReviewedSourceClaimRequest) { r.ExpectedSubject.ReviewDisplayArtifactID = "" }},
		{"empty_reason", func(r *AdmitReviewedSourceClaimRequest) { r.DecisionReason = "" }},
		{"whitespace_reason", func(r *AdmitReviewedSourceClaimRequest) { r.DecisionReason += " " }},
		{"nul_reason", func(r *AdmitReviewedSourceClaimRequest) { r.DecisionReason += "\x00" }},
		{"invalid_utf8_reason", func(r *AdmitReviewedSourceClaimRequest) { r.DecisionReason = string([]byte{0xff}) }},
		{"byte_not_rune_bound", func(r *AdmitReviewedSourceClaimRequest) { r.DecisionReason = strings.Repeat("字", 667) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := sourceReviewApprovedRequest(t)
			tc.mutate(&req)
			core := &fakeSourceReviewCore{}
			_, err := sourceReviewTestServer(core).AdmitReviewedSourceClaim(t.Context(), req)
			var toolErr *ToolError
			if !errors.As(err, &toolErr) || toolErr.Code != toolErrorInvalidRequest || core.writes != 0 || core.loads != 0 {
				t.Fatalf("invalid review entered core: %v", err)
			}
		})
	}
}

func TestSourceClaimReviewAdapterErrorsAndCancellation(t *testing.T) {
	snapshot, _, subject := sourceReviewGolden(t)
	get := GetSourceClaimReviewRequest{ExtractionAttemptID: snapshot.SubmissionReceipt.ExtractionAttemptID, ProposalOccurrenceID: subject.ReviewSubject.ProposalOccurrenceID}
	core := &fakeSourceReviewCore{err: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorReviewContractConflict, Message: "retained native conflict"}}
	server := sourceReviewTestServer(core)
	for _, call := range []func() error{
		func() error { _, err := server.GetSourceClaimReview(t.Context(), get); return err },
		func() error {
			_, err := server.AdmitReviewedSourceClaim(t.Context(), sourceReviewApprovedRequest(t))
			return err
		},
	} {
		var toolErr *ToolError
		if err := call(); !errors.As(err, &toolErr) || toolErr.Code != string(evidenceingestion.ErrorReviewContractConflict) {
			t.Fatalf("native conflict mapping: %v", err)
		}
	}
	core.loads, core.writes = 0, 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := server.GetSourceClaimReview(ctx, get); err == nil || core.loads != 0 {
		t.Fatalf("canceled read called core: %v", err)
	}
	if _, err := server.AdmitReviewedSourceClaim(ctx, sourceReviewApprovedRequest(t)); err == nil || core.writes != 0 {
		t.Fatalf("canceled writer called core: %v", err)
	}
	if _, err := (&Server{}).GetSourceClaimReview(t.Context(), get); err == nil {
		t.Fatal("unconfigured read succeeded")
	}
	server.sourceReview = nil
	if _, err := server.AdmitReviewedSourceClaim(t.Context(), sourceReviewApprovedRequest(t)); err == nil {
		t.Fatal("unconfigured writer succeeded")
	}
}

func TestSourceClaimReviewCompleteEnvelopeByteBoundary(t *testing.T) {
	// Transport-size proof only: native snapshot validity is covered separately
	// by the immutable golden and real PG fixtures. This is not fake admission.
	response := GetSourceClaimReviewResponse{ContractVersion: SourceClaimReviewMCPContract}
	base, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	response.Display.PayloadUTF8 = strings.Repeat("a", SourceClaimReviewMaxResponseBytes-len(base))
	encoded, err := marshalSourceClaimReviewResponse(response)
	if err != nil || len(encoded) != SourceClaimReviewMaxResponseBytes {
		t.Fatalf("exact bound: bytes=%d err=%v", len(encoded), err)
	}
	response.Display.PayloadUTF8 += "a"
	encoded, err = marshalSourceClaimReviewResponse(response)
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolErrorInvalidRequest || encoded != nil {
		t.Fatalf("overflow returned partial envelope: bytes=%d err=%v", len(encoded), err)
	}
}
