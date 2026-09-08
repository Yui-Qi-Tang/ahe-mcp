package evidenceingestionmcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

type fakeReviewedDispositionCore struct {
	fakeSourceReviewCore
	dispositions int
	input        evidenceingestion.ReviewedSourceClaimDispositionInput
	result       evidenceingestion.ProposalDispositionResult
}

func (f *fakeReviewedDispositionCore) RecordReviewedSourceClaimDisposition(_ context.Context, input evidenceingestion.ReviewedSourceClaimDispositionInput) (evidenceingestion.ProposalDispositionResult, error) {
	f.dispositions++
	f.input = input
	return f.result, f.err
}

func reviewedDispositionRequest(t *testing.T, decision string) RecordReviewedSourceClaimDispositionRequest {
	t.Helper()
	approved := sourceReviewApprovedRequest(t)
	return RecordReviewedSourceClaimDispositionRequest{ExtractionAttemptID: approved.ExtractionAttemptID,
		ExpectedSubject: approved.ExpectedSubject, Decision: decision, DecisionReason: "SYNTHETIC decision fixture; not authenticated human review"}
}

func TestReviewedDispositionDelegatesExactAtomicDecisionAndReplay(t *testing.T) {
	for _, tc := range []struct{ decision, outcome string }{{"reject", "rejected"}, {"audit_only", "audit_only"}} {
		t.Run(tc.decision, func(t *testing.T) {
			req := reviewedDispositionRequest(t, tc.decision)
			core := &fakeReviewedDispositionCore{result: evidenceingestion.ProposalDispositionResult{
				ProposalOccurrenceID: req.ExpectedSubject.ReviewSubject.ProposalOccurrenceID, AdmissionDecisionID: "adm:test",
				AdmissionOutcome: tc.outcome, DecisionBy: "reviewer:launcher", DecisionReason: req.DecisionReason, Replayed: true}}
			server := &Server{sourceReview: core, reviewPrincipal: runtimeauth.Principal{ID: "reviewer:launcher"}}
			payload, _ := json.Marshal(req)
			wire, err := server.CallTool(t.Context(), ToolRecordReviewedSourceClaimDisposition, payload)
			if err != nil {
				t.Fatal(err)
			}
			want := evidenceingestion.ReviewedSourceClaimDispositionInput{ExtractionAttemptID: req.ExtractionAttemptID,
				ExpectedSubject: req.ExpectedSubject, Outcome: tc.outcome, DecisionBy: "reviewer:launcher", DecisionReason: req.DecisionReason}
			if core.input != want || core.dispositions != 1 || core.loads != 0 || core.writes != 0 {
				t.Fatal("disposition must delegate once without a pending-only pre-read or admission")
			}
			var got RecordReviewedSourceClaimDispositionResponse
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			if got.ProposalOccurrenceID != core.result.ProposalOccurrenceID || got.AdmissionDecisionID != core.result.AdmissionDecisionID ||
				got.AdmissionOutcome != tc.outcome || got.DecisionBy != want.DecisionBy || got.DecisionReason != want.DecisionReason || !got.Replayed {
				t.Fatalf("disposition wire differs: %+v", got)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 6 || fields["canonical_ref"] != nil || fields["raw_evidence_node_ids"] != nil || fields["canonical_edge_ids"] != nil {
				t.Fatal("noncanonical disposition response exposed canonical fields")
			}
		})
	}
}

func TestReviewedDispositionAuthorizationAndStrictBoundary(t *testing.T) {
	req := reviewedDispositionRequest(t, "reject")
	encoded, _ := json.Marshal(req)
	valid := string(encoded)
	payloads := []string{"not JSON", "null", "{}", "{} {}",
		strings.Replace(valid, `"decision":"reject"`, `"decision":"reject","Decision":"audit_only"`, 1),
		strings.Replace(valid, `"decision":"reject"`, `"decision":"reject","decisi\u006fn":"audit_only"`, 1),
		strings.Replace(valid, `"decision":"reject"`, `"decision":"reject","decision_by":"spoof"`, 1),
		strings.Replace(valid, `"expected_subject":{`, `"expected_subject":{"complete":true,`, 1),
		strings.Replace(valid, `"decision":"reject"`, `"decision":"approved"`, 1),
		strings.Replace(valid, `"decision":"reject"`, `"decision":"pending"`, 1),
		strings.Replace(valid, `"decision":"reject"`, `"decision":"rejected"`, 1),
		strings.Replace(valid, `"decision":"reject"`, `"decision":""`, 1),
		strings.Replace(valid, req.ExpectedSubject.ReviewDisplayArtifactID, "", 1),
		strings.Replace(valid, req.DecisionReason, "", 1),
		strings.Repeat(" ", maxReviewToolRequestBytes+1)}
	for index, payload := range payloads {
		core := &fakeReviewedDispositionCore{}
		server := &Server{sourceReview: core}
		if _, err := server.CallTool(t.Context(), ToolRecordReviewedSourceClaimDisposition, []byte(payload)); !errors.Is(err, runtimeauth.ErrUnauthenticated) {
			t.Fatalf("payload %d authorization must precede decode: %v", index, err)
		}
		server.reviewPrincipal = runtimeauth.Principal{ID: "reviewer:test"}
		got, err := server.CallTool(t.Context(), ToolRecordReviewedSourceClaimDisposition, []byte(payload))
		var toolErr *ToolError
		if !errors.As(err, &toolErr) || toolErr.Code != toolErrorInvalidRequest || got != nil || core.dispositions != 0 || core.loads != 0 || core.writes != 0 {
			t.Fatalf("payload %d escaped strict boundary: %v", index, err)
		}
	}
}

func TestReviewedDispositionErrorsDoNotReturnPartialSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"nil_context", nil, nil},
		{"core_error", t.Context(), errors.New("private synthetic database diagnostic")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := &fakeReviewedDispositionCore{fakeSourceReviewCore: fakeSourceReviewCore{err: tc.err},
				result: evidenceingestion.ProposalDispositionResult{AdmissionOutcome: "rejected"}}
			server := &Server{sourceReview: core, reviewPrincipal: runtimeauth.Principal{ID: "reviewer:test"}}
			got, err := server.RecordReviewedSourceClaimDisposition(tc.ctx, reviewedDispositionRequest(t, "reject"))
			if err == nil || !reflect.DeepEqual(got, RecordReviewedSourceClaimDispositionResponse{}) {
				t.Fatal("failed disposition returned partial success")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	core := &fakeReviewedDispositionCore{}
	server := &Server{sourceReview: core, reviewPrincipal: runtimeauth.Principal{ID: "reviewer:test"}}
	if _, err := server.RecordReviewedSourceClaimDisposition(ctx, reviewedDispositionRequest(t, "reject")); err == nil || core.dispositions != 0 {
		t.Fatal("canceled call reached core")
	}
	server.sourceReview = &fakeSourceReviewCore{}
	if _, err := server.RecordReviewedSourceClaimDisposition(t.Context(), reviewedDispositionRequest(t, "reject")); err == nil {
		t.Fatal("missing narrow disposition capability succeeded")
	}
}
