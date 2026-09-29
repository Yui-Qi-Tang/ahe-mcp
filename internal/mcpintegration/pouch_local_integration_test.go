//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/pouchvalidation"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This test uses real native MCP intake/review/query and a restricted endpoint
// pool. It does not claim that the new Go API is a new public MCP admission tool.
func TestIntegrationPouchLocalPipeline(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("explicit disposable acceptance database required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	record := func(name string, value any) {
		if directory := os.Getenv("POUCH_LOCAL_EVIDENCE"); directory != "" {
			raw, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, name+".json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileEndpointReviewer)
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.intake, f.schema, "intake")
	reviewer := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.reviewer, f.schema, "source-claim-reviewer")
	config, err := pgxpool.ParseConfig(f.endpoints.dsn)
	if err != nil {
		t.Fatal("invalid isolated endpoint configuration")
	}
	endpoint, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: f.endpoints.group, Schema: f.schema, Profile: dbrole.ProfileEndpointReviewer})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(endpoint.Close)
	propose := func(id, statement string) evidenceingestionmcp.SubmitExtractorOutputResponse {
		source := authorityProcessTool[evidenceingestionmcp.SubmitExternalSourceResponse](t, intake, "submit_external_source", evidenceingestion.ExternalSourceEnvelopeV1{
			SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: id + "-source",
			SourceSystem: "synthetic", SourceNamespace: "pouch-local-product", ObjectType: "declared_contract", ObjectID: id, Revision: "v1",
			SourceLocation: "urn:pouch:local:" + id, Title: "Explicit synthetic product acceptance contract",
			ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
			Content: statement, Coverage: evidenceingestion.ExternalSourceCoverageExactExcerpt, Limitations: []string{"Synthetic declaration; not observed fact or source entailment"},
			CollectorID: "pouch-local", ConnectorID: "synthetic", ObservedAt: "2026-09-29T00:00:00Z",
		})
		refs := make([]string, len(source.Spans))
		for i, s := range source.Spans {
			refs[i] = s.SpanID
		}
		return authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", map[string]any{"request_id": id + "-proposal", "source_snapshot_id": source.SourceSnapshotID, "extraction_view_id": source.ExtractionViewID, "extractor_definition": map[string]any{"name": "synthetic-pouch-local", "version": "v1", "config": map[string]string{}}, "extractor_output": map[string]any{"proposals": []any{map[string]any{"proposal_local_id": "one", "statement_text": statement, "evidence_refs": refs}}}})
	}
	admitSource := func(p evidenceingestionmcp.SubmitExtractorOutputResponse) string {
		r := authorityProcessTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, "get_source_claim_review", evidenceingestionmcp.GetSourceClaimReviewRequest{ExtractionAttemptID: p.ExtractionAttemptID, ProposalOccurrenceID: p.ProposalOccurrenceID})
		receipt := authorityProcessTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", evidenceingestionmcp.AdmitReviewedSourceClaimRequest{ExtractionAttemptID: p.ExtractionAttemptID, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: isolated synthetic product acceptance, no human or semantic certification"})
		return receipt.CanonicalRef
	}
	marshal := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	for _, count := range []int{128, 129} {
		t.Run(fmt.Sprintf("source_refs_%d", count), func(t *testing.T) {
			lines := make([]string, count)
			for i := range lines {
				lines[i] = fmt.Sprintf("Synthetic finite model capacity line %03d; rules are an explicit caller declaration.", i)
			}
			sourceText := strings.Join(lines, "\n")
			parent := admitSource(propose(fmt.Sprintf("basis-%d", count), sourceText))
			// A fresh native Query must reconstruct the exact source before model use.
			query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
			original := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": parent})
			if original.StatementText != sourceText || len(original.SourceRefs) != count {
				t.Fatal("native source readback differs")
			}
			query.finish(t)
			c, p := pouchLocalFixture(parent, sourceText)
			rawAuthority := marshal(c)
			p.AuthoritySHA256 = pouchvalidation.Digest(rawAuthority)
			authority, err := pouchvalidation.NewAuthority(rawAuthority, p.AuthoritySHA256)
			if err != nil {
				t.Fatal(err)
			}
			raw := marshal(p)
			verified, err := authority.Validate(ctx, "local", raw)
			if err != nil {
				t.Fatal(err)
			}
			proposal := propose(fmt.Sprintf("result-%d", count), verified.Statement())
			req := evidenceingestion.EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: proposal.ProposalOccurrenceID, Derivation: &evidenceingestion.EndpointDerivation{ParentNodeIDs: []string{parent}, Method: pouchvalidation.Version, Producer: "ahe-pouch-validator", TraceRef: "pouch:sha256:" + verified.PackageSHA256}}
			before := relationCounts(t, ctx, f.pool)
			review, err := evidenceingestion.LoadPouchEndpointReview(ctx, endpoint, authority, "local", raw, req)
			if count == 129 {
				var domain *evidenceingestion.DomainError
				if !errors.As(err, &domain) || domain.Kind != evidenceingestion.ErrorInvalidInput {
					t.Fatalf("129 refs error: %v", err)
				}
				if relationCounts(t, ctx, f.pool) != before {
					t.Fatal("129 refs rejection wrote state")
				}
				record("native-129", map[string]any{"source": original, "reason": err.Error(), "canonical_unchanged": true})
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			approve := evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: "pouch-local-admit", Review: req, ExpectedSubject: review.Native.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: exact local finite conclusion only", ReviewerID: "synthetic:pouch-product-reviewer"}
			for _, mutation := range []string{"missing_approval", "wrong_subject", "old_source", "false_no_return", "wrong_effect", "false_shortest"} {
				t.Run(mutation, func(t *testing.T) {
					cp := approve
					packet := p
					candidate := raw
					switch mutation {
					case "missing_approval":
						cp.Decision = ""
					case "wrong_subject":
						cp.ExpectedSubject += "0"
					case "old_source":
						packet.SourceBinding = map[string]string{"canon-node:old": pouchvalidation.Digest([]byte(sourceText))}
						candidate = marshal(packet)
					case "false_no_return":
						packet.ReturnKind = "no_return"
						packet.Return = nil
						packet.ClosedStates = []pouchvalidation.State{{"x": "2"}}
						candidate = marshal(packet)
					case "wrong_effect":
						packet.Forward = pouchvalidation.Trace{States: []pouchvalidation.State{{"x": "0"}, {"x": "1"}}, Actions: []string{"direct"}, ClaimShortest: true}
						candidate = marshal(packet)
					case "false_shortest":
						packet.Forward = pouchvalidation.Trace{States: []pouchvalidation.State{{"x": "0"}, {"x": "1"}, {"x": "2"}}, Actions: []string{"first", "second"}, ClaimShortest: true}
						candidate = marshal(packet)
					}
					_, e := evidenceingestion.AdmitReviewedPouchEndpoint(ctx, endpoint, authority, "local", candidate, cp)
					if e == nil {
						t.Fatal("invalid result admitted")
					}
					reasons := map[string]string{"old_source": "SOURCE_BINDING", "false_no_return": "FALSE_NO_RETURN", "wrong_effect": "TRACE_EFFECT_OR_FRAME", "false_shortest": "FORWARD_NOT_SHORTEST"}
					if want, ok := reasons[mutation]; ok {
						var rejection *pouchvalidation.Rejection
						if !errors.As(e, &rejection) || rejection.Code != want {
							t.Fatalf("got %v, want %s", e, want)
						}
					} else {
						var domain *evidenceingestion.DomainError
						want := evidenceingestion.ErrorInvalidInput
						if mutation == "wrong_subject" {
							want = evidenceingestion.ErrorReviewContractConflict
						}
						if !errors.As(e, &domain) || domain.Kind != want {
							t.Fatalf("wrong native refusal: %v", e)
						}
					}
					if relationCounts(t, ctx, f.pool) != before {
						t.Fatal("refusal changed canonical state")
					}
					record(mutation, map[string]any{"reason": e.Error(), "canonical_unchanged": true})
				})
			}
			t.Run("wrong_native_source_digest", func(t *testing.T) {
				c.SourceBinding = map[string]string{parent: pouchvalidation.Digest([]byte("wrong original source"))}
				packet := p
				packet.SourceBinding = c.SourceBinding
				b := marshal(c)
				packet.AuthoritySHA256 = pouchvalidation.Digest(b)
				other, e := pouchvalidation.NewAuthority(b, packet.AuthoritySHA256)
				if e != nil {
					t.Fatal(e)
				}
				v, e := other.Validate(ctx, "local", marshal(packet))
				if e != nil {
					t.Fatal(e)
				}
				wrongProposal := propose("wrong-native-source", v.Statement())
				badReq := req
				deriv := *req.Derivation
				deriv.TraceRef = "pouch:sha256:" + v.PackageSHA256
				badReq.Derivation = &deriv
				badReq.ProposalOccurrenceID = wrongProposal.ProposalOccurrenceID
				afterPending := relationCounts(t, ctx, f.pool)
				if _, e = evidenceingestion.LoadPouchEndpointReview(ctx, endpoint, other, "local", marshal(packet), badReq); e == nil {
					t.Fatal("wrong source digest accepted")
				}
				if relationCounts(t, ctx, f.pool) != afterPending {
					t.Fatal("source mismatch wrote canonical state")
				}
			})
			for _, fault := range []string{"timeout", "exit_nonzero", "malformed_json", "missing_receipt", "wrong_package_digest", "known_refusal_then_transport"} {
				t.Run("consumer_"+fault, func(t *testing.T) {
					snapshot := relationCounts(t, ctx, f.pool)
					timeout := 2 * time.Second
					if fault == "timeout" {
						timeout = 100 * time.Millisecond
					}
					subctx, stop := context.WithTimeout(ctx, timeout)
					defer stop()
					cmd := exec.CommandContext(subctx, os.Args[0], "-test.run=^TestPouchInjectedDelivery$")
					cmd.Env = []string{"POUCH_TEST_DELIVERY=" + fault}
					knownReason := ""
					if fault == "known_refusal_then_transport" {
						bad := p
						bad.ReturnKind = "no_return"
						bad.Return = nil
						bad.ClosedStates = []pouchvalidation.State{{"x": "2"}}
						_, refusal := authority.Validate(ctx, "local", marshal(bad))
						var rejection *pouchvalidation.Rejection
						if !errors.As(refusal, &rejection) || rejection.Code != "FALSE_NO_RETURN" {
							t.Fatal("known semantic rejection missing")
						}
						knownReason = rejection.Code
					}
					output, processErr := cmd.Output()
					if cmd.ProcessState == nil {
						t.Fatal("consumer fault process did not start")
					}
					var delivery struct {
						Status  string                   `json:"status"`
						Receipt *pouchvalidation.Receipt `json:"receipt"`
					}
					d := json.NewDecoder(bytes.NewReader(output))
					d.DisallowUnknownFields()
					decodeErr := d.Decode(&delivery)
					accepted := processErr == nil && decodeErr == nil && delivery.Status == "ACCEPTED" && delivery.Receipt != nil && reflect.DeepEqual(*delivery.Receipt, verified)
					if accepted {
						t.Fatal("faulty consumer delivery accepted")
					}
					if fault == "timeout" && !errors.Is(subctx.Err(), context.DeadlineExceeded) {
						t.Fatal("timeout was not observed")
					}
					if relationCounts(t, ctx, f.pool) != snapshot {
						t.Fatal("delivery fault changed canonical state")
					}
					t.Logf("delivery=%s admission_attempts=0 prior_semantic_rejection=%s", fault, knownReason)
					record("consumer-"+fault, map[string]any{"exit": cmd.ProcessState.ExitCode(), "stdout": string(output), "deadline_exceeded": errors.Is(subctx.Err(), context.DeadlineExceeded), "prior_semantic_rejection": knownReason, "admission_attempts": 0, "canonical_unchanged": true})
				})
			}
			t.Run("native_cancel_before_admission", func(t *testing.T) {
				cancelled, stop := context.WithCancel(ctx)
				stop()
				snapshot := relationCounts(t, ctx, f.pool)
				_, e := evidenceingestion.AdmitReviewedPouchEndpoint(cancelled, endpoint, authority, "local", raw, approve)
				if !errors.Is(e, context.Canceled) || relationCounts(t, ctx, f.pool) != snapshot {
					t.Fatal("cancelled admission changed state")
				}
			})
			committed, err := evidenceingestion.AdmitReviewedPouchEndpoint(ctx, endpoint, authority, "local", raw, approve)
			if err != nil {
				t.Fatal(err)
			}
			// Deliberately lose the delivery at this boundary, not the committed receipt.
			// A later transport failure must never be reported as no native write.
			afterCommit := relationCounts(t, ctx, f.pool)
			replay, err := evidenceingestion.AdmitReviewedPouchEndpoint(ctx, endpoint, authority, "local", raw, approve)
			if err != nil || !replay.Native.Admission.Replayed || replay.Native.Admission.CanonicalRef != committed.Native.Admission.CanonicalRef || relationCounts(t, ctx, f.pool) != afterCommit {
				t.Fatalf("lost-ack replay: %v", err)
			}
			fresh := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
			readback := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, fresh, "get_evidence_record", map[string]any{"canonical_id": committed.Native.Admission.CanonicalRef})
			if readback.StatementText != verified.Statement() || readback.EndpointAdmission == nil || readback.Canonical == nil || readback.Canonical.NodeKind != "derived_claim" || !reflect.DeepEqual(readback.Canonical.Provenance.OriginRefs, []string{parent}) {
				t.Fatal("fresh native result readback mismatch")
			}
			fresh.finish(t)
			record("native-128", map[string]any{"source": original, "authority": json.RawMessage(rawAuthority), "package": json.RawMessage(raw), "review": review, "admission": committed, "replay": replay, "readback": readback})
			// DB roles enforce operation separation, not OS-user separation.
			intake.assertDenied(t, "admit_reviewed_endpoint", approve)
			reviewer.assertDenied(t, "admit_reviewed_endpoint", approve)
		})
	}
	intake.finish(t)
	reviewer.finish(t)
}
func pouchLocalFixture(parent, text string) (pouchvalidation.Contract, pouchvalidation.Package) {
	s0, s1, s2 := pouchvalidation.State{"x": "0"}, pouchvalidation.State{"x": "1"}, pouchvalidation.State{"x": "2"}
	c := pouchvalidation.Contract{SchemaVersion: pouchvalidation.Version, RequestID: "local", SourceBinding: map[string]string{parent: pouchvalidation.Digest([]byte(text))}, Assumptions: map[string]string{"finite": "scenario_assumption"}, Model: pouchvalidation.Model{Fields: map[string][]string{"x": {"0", "1", "2"}}, Forbidden: []pouchvalidation.State{}, Actions: []pouchvalidation.Action{{ID: "first", Guard: s0, Effect: s1, Frame: []string{}}, {ID: "second", Guard: s1, Effect: s2, Frame: []string{}}, {ID: "direct", Guard: s0, Effect: s2, Frame: []string{}}, {ID: "restore", Guard: s2, Effect: s0, Frame: []string{}}}}, Start: s0, Goal: s2, Baseline: s0, ForwardLimit: 2, ReturnLimit: 2}
	p := pouchvalidation.Package{SchemaVersion: pouchvalidation.Version, RequestID: "local", SourceBinding: c.SourceBinding, Assumptions: c.Assumptions, Forward: pouchvalidation.Trace{States: []pouchvalidation.State{s0, s2}, Actions: []string{"direct"}, ClaimShortest: true}, ReturnKind: "return", Return: &pouchvalidation.Trace{States: []pouchvalidation.State{s2, s0}, Actions: []string{"restore"}, ClaimShortest: true}, ClosedStates: []pouchvalidation.State{}}
	return c, p
}

// TestPouchInjectedDelivery supplies explicit process/protocol faults, not a
// fake semantic verdict. Its child environment contains no database credential.
func TestPouchInjectedDelivery(t *testing.T) {
	mode := os.Getenv("POUCH_TEST_DELIVERY")
	if mode == "" {
		t.Skip("child fault mode only")
	}
	switch mode {
	case "timeout":
		time.Sleep(30 * time.Second)
	case "exit_nonzero":
		fmt.Print(`{"status":"ACCEPTED"}`)
		os.Exit(9)
	case "malformed_json":
		fmt.Print(`{`)
	case "missing_receipt":
		fmt.Print(`{"status":"ACCEPTED"}`)
	case "wrong_package_digest":
		fmt.Print(`{"status":"ACCEPTED","receipt":{"package_sha256":"wrong"}}`)
	case "known_refusal_then_transport":
		os.Exit(8)
	default:
		os.Exit(10)
	}
	os.Exit(0)
}
