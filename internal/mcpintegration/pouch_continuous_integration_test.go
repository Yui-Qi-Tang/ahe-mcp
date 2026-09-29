//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/pouchvalidation"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestIntegrationPouchContinuous is an opt-in local acceptance test. Its operator
// supplies a frozen Pouch runner and original public/synthetic sources. No saved
// path is supplied: the runner searches only after native source readback and
// after the caller has fixed its authority. No database credentials reach Python.
func TestIntegrationPouchContinuous(t *testing.T) {
	root, dsn := os.Getenv("POUCH_CONTINUOUS_RUN"), os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if root == "" || dsn == "" {
		t.Skip("explicit frozen run and isolated acceptance database required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Second)
	defer cancel()
	var input struct {
		RunID string `json:"run_id"`
		Cases []struct {
			ID               string `json:"id"`
			ReturnLimit      int    `json:"return_limit"`
			ExpectedPackages int    `json:"expected_packages"`
			Sources          []struct {
				ID       string                                     `json:"id"`
				SHA256   string                                     `json:"sha256"`
				Envelope evidenceingestion.ExternalSourceEnvelopeV1 `json:"envelope"`
			} `json:"sources"`
		} `json:"cases"`
	}
	pouchContinuousRead(t, filepath.Join(root, "frozen", "inputs.json"), &input)
	if input.RunID != filepath.Base(root) || len(input.Cases) != 2 {
		t.Fatal("run identity or fixed case denominator mismatch")
	}
	for _, c := range input.Cases {
		t.Run(c.ID, func(t *testing.T) {
			out := filepath.Join(root, c.ID)
			if err := os.Mkdir(out, 0700); err != nil {
				t.Fatal(err)
			}
			f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileEndpointReviewer)
			intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.intake, f.schema, "intake")
			reviewer := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.reviewer, f.schema, "source-claim-reviewer")
			pouchContinuousDiscover(t, intake, "submit_external_source", "submit_extractor_output")
			pouchContinuousDiscover(t, reviewer, "get_source_claim_review", "admit_reviewed_source_claim")
			config, err := pgxpool.ParseConfig(f.endpoints.dsn)
			if err != nil {
				t.Fatal("invalid isolated endpoint configuration")
			}
			endpoint, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: f.endpoints.group, Schema: f.schema, Profile: dbrole.ProfileEndpointReviewer})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(endpoint.Close)
			propose := func(id, statement string, source evidenceingestionmcp.SubmitExternalSourceResponse) evidenceingestionmcp.SubmitExtractorOutputResponse {
				refs := make([]string, len(source.Spans))
				for i, s := range source.Spans {
					refs[i] = s.SpanID
				}
				return authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", map[string]any{"request_id": id, "source_snapshot_id": source.SourceSnapshotID, "extraction_view_id": source.ExtractionViewID, "extractor_definition": map[string]any{"name": "pouch-continuous-local", "version": "v1", "config": map[string]string{}}, "extractor_output": map[string]any{"proposals": []any{map[string]any{"proposal_local_id": "one", "statement_text": statement, "evidence_refs": refs}}}})
			}
			ids := map[string]string{}
			bindings := map[string]string{}
			texts := map[string]string{}
			var modelSource evidenceingestionmcp.SubmitExternalSourceResponse
			for _, s := range c.Sources {
				if pouchvalidation.Digest([]byte(s.Envelope.Content)) != s.SHA256 {
					t.Fatal("frozen source digest differs")
				}
				source := authorityProcessTool[evidenceingestionmcp.SubmitExternalSourceResponse](t, intake, "submit_external_source", s.Envelope)
				p := propose(input.RunID+"-"+s.ID, s.Envelope.Content, source)
				r := authorityProcessTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, "get_source_claim_review", evidenceingestionmcp.GetSourceClaimReviewRequest{ExtractionAttemptID: p.ExtractionAttemptID, ProposalOccurrenceID: p.ProposalOccurrenceID})
				a := authorityProcessTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", evidenceingestionmcp.AdmitReviewedSourceClaimRequest{ExtractionAttemptID: p.ExtractionAttemptID, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: isolated public/synthetic declaration; no human review or semantic certification"})
				ids[s.ID] = a.CanonicalRef
				bindings[a.CanonicalRef] = s.SHA256
				texts[a.CanonicalRef] = s.Envelope.Content
				if s.ID == c.ID+"-model" {
					modelSource = source
				}
				pouchContinuousSave(t, filepath.Join(out, "source-"+s.ID+".json"), map[string]any{"capture": source, "proposal": p, "review": r, "admission": a})
			}
			reviewer.finish(t)
			// A separate native process, not the intake response, supplies the model.
			query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
			pouchContinuousDiscover(t, query, "get_evidence_record")
			readbacks := map[string]evidencequerymcp.GetEvidenceRecordResponse{}
			for alias, id := range ids {
				r := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": id})
				if r.StatementText != texts[id] || pouchvalidation.Digest([]byte(r.StatementText)) != bindings[id] {
					t.Fatal("native source readback mismatch")
				}
				readbacks[alias] = r
			}
			query.finish(t)
			pouchContinuousSave(t, filepath.Join(out, "source-readbacks.json"), readbacks)
			modelText := readbacks[c.ID+"-model"].StatementText
			if modelText == "" || modelSource.SourceSnapshotID == "" {
				t.Fatal("model source missing")
			}
			contract := pouchContinuousContract(t, modelText, input.RunID+"/"+c.ID, bindings, ids, c.ReturnLimit)
			authorityRaw := pouchContinuousSave(t, filepath.Join(out, "authority.json"), contract)
			authorityHash := pouchvalidation.Digest(authorityRaw)
			authority, err := pouchvalidation.NewAuthority(authorityRaw, authorityHash)
			if err != nil {
				t.Fatal(err)
			}
			pouchContinuousSave(t, filepath.Join(out, "caller-pin.json"), map[string]string{"request_id": contract.RequestID, "authority_sha256": authorityHash})
			pouchContinuousSave(t, filepath.Join(out, "native-model.json"), json.RawMessage(modelText))
			// Record the attempt before starting a tool. A failed process cannot erase
			// its attempt, timeout, output or already obtained native source receipts.
			pouchContinuousSave(t, filepath.Join(out, "generator-attempt.json"), map[string]any{"status": "STARTED", "authority_sha256": authorityHash, "request_id": contract.RequestID})
			subctx, stop := context.WithTimeout(ctx, 120*time.Second)
			cmd := exec.CommandContext(subctx, "python3", "-I", "-B", filepath.Join(root, "frozen", "driver.py"), out)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1"}
			output, processErr := cmd.CombinedOutput()
			stop()
			if err := os.WriteFile(filepath.Join(out, "generator.log"), output, 0600); err != nil {
				t.Fatal(err)
			}
			exit := -1
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
			}
			pouchContinuousSave(t, filepath.Join(out, "generator-exit.json"), map[string]any{"exit": exit, "deadline_exceeded": errors.Is(subctx.Err(), context.DeadlineExceeded), "authority_sha256": authorityHash})
			if processErr != nil {
				t.Fatalf("Pouch generation failed; retained exit=%d and output", exit)
			}
			var generated struct {
				Status   string   `json:"status"`
				Packages []string `json:"packages"`
			}
			pouchContinuousRead(t, filepath.Join(out, "generation.json"), &generated)
			if generated.Status != "PASS" || len(generated.Packages) != c.ExpectedPackages {
				t.Fatal("generation result or denominator differs")
			}
			for i, name := range generated.Packages {
				if name != fmt.Sprintf("package-%03d.json", i) {
					t.Fatal("unexpected package path")
				}
				raw, err := os.ReadFile(filepath.Join(out, name))
				if err != nil {
					t.Fatal(err)
				}
				v, err := authority.Validate(ctx, contract.RequestID, raw)
				if err != nil {
					t.Fatal(err)
				}
				pouchContinuousSave(t, filepath.Join(out, fmt.Sprintf("validation-%03d.json", i)), v)
				// Mutations pass through the real validated admission entry point and
				// must fail for the intended reason before any native result write.
				if i == 0 {
					var packet pouchvalidation.Package
					if err := json.Unmarshal(raw, &packet); err != nil {
						t.Fatal(err)
					}
					for _, fault := range []string{"old_source", "false_no_return", "wrong_effect"} {
						bad := packet
						want := ""
						switch fault {
						case "old_source":
							bad.SourceBinding = map[string]string{"canon-node:prior-run": pouchvalidation.Digest([]byte("old"))}
							want = "SOURCE_BINDING"
						case "false_no_return":
							if packet.ReturnKind != "return" {
								t.Fatal("first path must have a return for this fixed mutation")
							}
							bad.ReturnKind = "no_return"
							bad.Return = nil
							bad.ClosedStates = []pouchvalidation.State{packet.Forward.States[len(packet.Forward.States)-1]}
							want = "FALSE_NO_RETURN"
						case "wrong_effect":
							bad.Forward.States = append([]pouchvalidation.State(nil), packet.Forward.States...)
							bad.Forward.States[1] = packet.Forward.States[len(packet.Forward.States)-1]
							want = "TRACE_EFFECT_OR_FRAME"
						}
						b := pouchContinuousSave(t, filepath.Join(out, "mutation-"+fault+".json"), bad)
						snapshot := relationCounts(t, ctx, f.pool)
						_, e := evidenceingestion.AdmitReviewedPouchEndpoint(ctx, endpoint, authority, contract.RequestID, b, evidenceingestion.ReviewedEndpointAdmissionInput{})
						var refusal *pouchvalidation.Rejection
						if !errors.As(e, &refusal) || refusal.Code != want || relationCounts(t, ctx, f.pool) != snapshot {
							t.Fatalf("mutation %s: got %v, want %s without writes", fault, e, want)
						}
						pouchContinuousSave(t, filepath.Join(out, "refusal-"+fault+".json"), map[string]any{"reason": refusal.Code, "package_sha256": pouchvalidation.Digest(b), "canonical_unchanged": true})
					}
				}
				proposal := propose(fmt.Sprintf("%s-%s-result-%03d", input.RunID, c.ID, i), v.Statement(), modelSource)
				req := evidenceingestion.EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: proposal.ProposalOccurrenceID, Derivation: &evidenceingestion.EndpointDerivation{ParentNodeIDs: authority.SourceIDs(), Method: pouchvalidation.Version, Producer: "ahe-pouch-validator", TraceRef: "pouch:sha256:" + v.PackageSHA256}}
				r, err := evidenceingestion.LoadPouchEndpointReview(ctx, endpoint, authority, contract.RequestID, raw, req)
				if err != nil {
					t.Fatal(err)
				}
				pouchContinuousSave(t, filepath.Join(out, fmt.Sprintf("admission-attempt-%03d.json", i)), map[string]any{"status": "STARTED", "package_sha256": v.PackageSHA256, "validation": v, "review": r})
				a, err := evidenceingestion.AdmitReviewedPouchEndpoint(ctx, endpoint, authority, contract.RequestID, raw, evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: fmt.Sprintf("%s-%s-admit-%03d", input.RunID, c.ID, i), Review: req, ExpectedSubject: r.Native.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: exact finite conclusion in isolated integration", ReviewerID: "synthetic:pouch-continuous"})
				if err != nil {
					t.Fatal(err)
				}
				pouchContinuousSave(t, filepath.Join(out, fmt.Sprintf("admission-%03d.json", i)), a)
			}
			intake.finish(t)
			fresh := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
			pouchContinuousDiscover(t, fresh, "get_evidence_record")
			for i := range generated.Packages {
				var a evidenceingestion.PouchEndpointReceipt
				pouchContinuousRead(t, filepath.Join(out, fmt.Sprintf("admission-%03d.json", i)), &a)
				r := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, fresh, "get_evidence_record", map[string]any{"canonical_id": a.Native.Admission.CanonicalRef})
				if r.StatementText != a.Validation.Statement() || r.EndpointAdmission == nil || r.Canonical == nil || r.Canonical.NodeKind != "derived_claim" || !reflect.DeepEqual(r.Canonical.Provenance.OriginRefs, authority.SourceIDs()) {
					t.Fatal("new native result readback mismatch")
				}
				pouchContinuousSave(t, filepath.Join(out, fmt.Sprintf("result-readback-%03d.json", i)), r)
			}
			for _, id := range ids {
				r := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, fresh, "get_evidence_record", map[string]any{"canonical_id": id})
				if r.StatementText != texts[id] {
					t.Fatal("source changed after derived admissions")
				}
			}
			fresh.finish(t)
			pouchContinuousSave(t, filepath.Join(out, "native-summary.json"), map[string]any{"status": "PASS", "sources": len(ids), "packages": len(generated.Packages), "mutations": 3, "new_query_readbacks": len(generated.Packages), "original_sources_unchanged": true})
		})
		if t.Failed() {
			break
		}
	}
}

func pouchContinuousDiscover(t *testing.T, p *authorityProcess, required ...string) {
	t.Helper()
	response := p.request(t, "tools/list", map[string]any{})
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &result) != nil {
		t.Fatal("tool discovery failed")
	}
	for _, name := range required {
		found := false
		for _, tool := range result.Tools {
			found = found || tool.Name == name
		}
		if !found {
			t.Fatalf("required tool unavailable: %s", name)
		}
	}
}

func pouchContinuousSave(t *testing.T, path string, value any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return b
}
func pouchContinuousRead(t *testing.T, path string, value any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, value); err != nil {
		t.Fatal(err)
	}
}

// The receiver-side adapter reads the actual model declaration returned by Core.
// List-valued forbidden patterns are expanded without changing their truth set;
// unsupported guards/effects or non-unit costs fail rather than being discarded.
func pouchContinuousContract(t *testing.T, text, request string, bindings, aliases map[string]string, returnLimit int) pouchvalidation.Contract {
	t.Helper()
	var m struct {
		Fields         map[string][]string              `json:"fields"`
		Baseline       pouchvalidation.State            `json:"baseline"`
		Goals          map[string]pouchvalidation.State `json:"goals"`
		ForwardHorizon int                              `json:"forward_horizon"`
		Forbidden      []map[string]json.RawMessage     `json:"domain_forbidden"`
		Actions        []struct {
			ID     string                `json:"id"`
			Guard  pouchvalidation.State `json:"guard"`
			Effect pouchvalidation.State `json:"effect"`
			Frame  []string              `json:"frame"`
			Cost   int                   `json:"cost"`
		} `json:"actions"`
		Basis []string `json:"direct_basis_ids"`
	}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Basis) != len(aliases) {
		t.Fatal("model source basis differs")
	}
	for _, id := range m.Basis {
		if aliases[id] == "" {
			t.Fatal("model basis is not in current readback")
		}
	}
	c := pouchvalidation.Contract{SchemaVersion: pouchvalidation.Version, RequestID: request, SourceBinding: bindings, Assumptions: map[string]string{"complete_original_declaration": text}, Model: pouchvalidation.Model{Fields: m.Fields, Forbidden: []pouchvalidation.State{}, Actions: []pouchvalidation.Action{}}, Start: m.Baseline, Baseline: m.Baseline, Goal: m.Goals["forward"], ForwardLimit: m.ForwardHorizon, ReturnLimit: returnLimit}
	b, err := json.Marshal(aliases)
	if err != nil {
		t.Fatal(err)
	}
	c.Assumptions["current_native_source_mapping"] = string(b)
	for _, a := range m.Actions {
		if a.Cost != 1 {
			t.Fatal("unsupported action cost")
		}
		c.Model.Actions = append(c.Model.Actions, pouchvalidation.Action{ID: a.ID, Guard: a.Guard, Effect: a.Effect, Frame: a.Frame})
	}
	for _, p := range m.Forbidden {
		expanded := []pouchvalidation.State{{}}
		keys := make([]string, 0, len(p))
		for k := range p {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var one string
			var values []string
			if json.Unmarshal(p[k], &one) == nil {
				values = []string{one}
			} else if json.Unmarshal(p[k], &values) != nil || len(values) == 0 {
				t.Fatal("unsupported forbidden pattern")
			}
			next := []pouchvalidation.State{}
			for _, state := range expanded {
				for _, v := range values {
					s := pouchvalidation.State{}
					for k, v := range state {
						s[k] = v
					}
					s[k] = v
					next = append(next, s)
				}
			}
			expanded = next
		}
		c.Model.Forbidden = append(c.Model.Forbidden, expanded...)
	}
	return c
}
