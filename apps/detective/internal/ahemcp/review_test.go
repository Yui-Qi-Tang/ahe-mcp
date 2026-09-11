package ahemcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

func reviewFixture(t *testing.T) (SourceClaimReview, *labstatus.Document, []labstatus.Record, Handoff) {
	t.Helper()
	doc, records := handoffDocument(t, "臺灣來源🙂\r\nsecond line\r\n")
	h := queryHandoff()
	h.SourceSnapshotID, h.ExtractionViewID, h.ExtractionAttemptID, h.ProposalOccurrenceID = fixtureID("srcsnap:"), fixtureID("view:"), fixtureID("attempt:"), fixtureID("occ:")
	q := queryFixture(doc, records[0], h)
	r := ReviewSubmissionReceipt{ContractVersion: reviewContract, ID: fixtureID("submission-receipt:v1:sha256:"), SourceSnapshotID: h.SourceSnapshotID, ExtractionViewID: h.ExtractionViewID, ExtractorDefinitionID: fixtureID("extractor:"), ExtractionRunID: fixtureID("run:"), ExtractionAttemptID: h.ExtractionAttemptID, ExtractionAttemptNumber: 1, ExtractorOutputHash: fixtureID("sha256:"), ProposalBatchID: fixtureID("batch:"), ProposalManifestID: fixtureID("proposal-manifest:v1:sha256:"), ProposalCount: 1}
	refs := []string{}
	for _, ref := range q.SourceRefs {
		refs = append(refs, ref.SpanID)
	}
	proposals := []proposal{{ProposalLocalID: "record-1", StatementText: records[0].Statement, EvidenceRefs: refs}}
	x := handoffExtractor()
	definition := extractorDefinition{Name: x.Name, Version: x.Version, Config: map[string]string{"model": x.Model, "source_mode": "frozen_local_snapshot"}}
	r.RequestID = requestID("proposal", "lab-status", doc.Source().SHA256, digest(definition), digest(proposals))
	r.ExtractorOutputHash = "sha256:" + digest(extractorOutput{Proposals: proposals})
	m := ReviewProposalManifest{ContractVersion: reviewContract, ID: r.ProposalManifestID, ProposalBatchID: r.ProposalBatchID, ExtractionAttemptID: r.ExtractionAttemptID, ExtractorOutputHash: r.ExtractorOutputHash, ProposalSetHash: fixtureID("sha256:"), ProposalCount: 1, Entries: []ReviewManifestEntry{{Ordinal: 1, ProposalOccurrenceID: h.ProposalOccurrenceID, ProposalLocalID: "record-1", ProposalFingerprint: fixtureID("fp:"), ProposalFingerprintVersion: "statement-v1", ProposalKind: "statement"}}}
	b := reviewBasis{ContractVersion: reviewContract, ID: fixtureID("proposal-basis:v1:sha256:"), SubmissionReceiptID: r.ID, ProposalManifestID: m.ID, ProposalManifestOrdinal: 1, ProposalOccurrenceID: h.ProposalOccurrenceID, ProposalLocalID: "record-1", ProposalFingerprint: m.Entries[0].ProposalFingerprint, ProposalFingerprintVersion: "statement-v1", ProposalKind: "statement", StatementText: records[0].Statement, SourceRefs: q.SourceRefs, SourceSnapshotID: h.SourceSnapshotID, ExtractionViewID: h.ExtractionViewID, SourceSystem: "manual_text", SourceID: "lab-status", SourceVersion: contentHash(doc.RawText()), RawContentHash: contentHash(doc.RawText()), RendererName: "manual-text-identity", RendererVersion: "v1", RenderedContentHash: contentHash(doc.RawText()), OriginMetadataHash: "sha256:" + digest(map[string]string{"capture_kind": "frozen_local_snapshot", "snapshot_sha256": doc.Source().SHA256}), SourceLimitations: []string{}, ExtractorDefinitionID: r.ExtractorDefinitionID, ExtractionRunID: r.ExtractionRunID, ExtractionAttemptID: r.ExtractionAttemptID, ExtractorName: x.Name, ExtractorVersion: x.Version, ExtractorConfigHash: q.Extractor.ConfigHash, SourceBindingKind: "source_snapshot"}
	p := reviewPackage{ContractVersion: reviewContract, ID: fixtureID("review-package:v1:sha256:"), ReviewTemplateID: "source-claim-review-card/v1", SubmissionReceiptID: r.ID, ProposalBasis: b, ProposedEffect: "admit-source-backed-statement/v1", Coverage: "One pending source-backed statement and every exact source span bound to it.", Limitations: reviewLimitations()}
	review := SourceClaimReview{ContractVersion: "source-claim-review-mcp/v1", SubmissionReceipt: r, ProposalManifest: m, Subject: ReviewSubject{ReviewSubject: ExactReviewSubject{SubmissionReceiptID: r.ID, ProposalManifestID: m.ID, ProposalOccurrenceID: h.ProposalOccurrenceID, ProposalBasisID: b.ID, ReviewPackageID: p.ID}}}
	setReviewPayload(t, &review, p)
	return review, doc, records, h
}

func fixtureID(prefix string) string { return prefix + strings.Repeat("a", 64) }
func setReviewPayload(t *testing.T, r *SourceClaimReview, p reviewPackage) {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	r.Display = ReviewDisplay{ContractVersion: "review-package-display/v1", ReviewPackageID: p.ID, MediaType: reviewMediaType, PayloadUTF8: string(data)}
	r.Display.ID = "review-display:v1:" + contentHash(reviewMediaType+"\n"+string(data))
	r.Subject.ReviewDisplayArtifactID = r.Display.ID
}
func fixtureAdmission(h Handoff) ReviewedAdmission {
	return ReviewedAdmission{ProposalOccurrenceID: h.ProposalOccurrenceID, AdmissionDecisionID: fixtureID("adm:"), AdmissionOutcome: "admitted", CanonicalRef: "canon-node:" + strings.Repeat("a", 16), RawEvidenceNodeIDs: []string{"canon-node:" + strings.Repeat("b", 16)}, CanonicalEdgeIDs: []string{"canon-edge:" + strings.Repeat("c", 16)}}
}

func TestReviewExactSavedSource(t *testing.T) {
	r, doc, records, h := reviewFixture(t)
	if err := ValidateSourceClaimReview(r, "lab-status", doc, handoffExtractor(), records, h); err != nil {
		t.Fatal(err)
	}
	launcher, trace := reviewLauncher(t, "read", r, fixtureAdmission(h), nil, nil)
	got, err := ReviewSourceClaim(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records, h)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("review mismatch: %v", err)
	}
	assertReviewOnlyCall(t, trace, "get_source_claim_review")
}

func TestReviewRejectsChangedMaterial(t *testing.T) {
	r, doc, records, h := reviewFixture(t)
	mutations := map[string]func(*SourceClaimReview){
		"contract":    func(r *SourceClaimReview) { r.ContractVersion = "next" },
		"receipt":     func(r *SourceClaimReview) { r.SubmissionReceipt.RequestID = "other" },
		"session":     func(r *SourceClaimReview) { r.SubmissionReceipt.ProducerSessionRef = "unexpected" },
		"batch count": func(r *SourceClaimReview) { r.ProposalManifest.ProposalCount = 2 },
		"manifest":    func(r *SourceClaimReview) { r.ProposalManifest.ID = fixtureID("wrong:") },
		"subject":     func(r *SourceClaimReview) { r.Subject.ReviewSubject.ProposalOccurrenceID = fixtureID("other:") },
		"media":       func(r *SourceClaimReview) { r.Display.MediaType = "text/plain" },
		"payload":     func(r *SourceClaimReview) { r.Display.PayloadUTF8 += " " },
		"digest":      func(r *SourceClaimReview) { r.Display.ID = fixtureID("review-display:v1:sha256:") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := r
			mutate(&copy)
			if ValidateSourceClaimReview(copy, "lab-status", doc, handoffExtractor(), records, h) == nil {
				t.Fatal("drift accepted")
			}
		})
	}
	for _, path := range []string{"statement_text", "source_id", "source_version", "raw_content_hash", "rendered_content_hash", "origin_metadata_hash", "extractor_name", "extractor_version", "extractor_config_hash", "renderer_name", "renderer_version", "source_title", "source_location", "source_coverage", "source_binding_kind", "source_system"} {
		t.Run(path, func(t *testing.T) {
			copy := r
			var object map[string]any
			_ = json.Unmarshal([]byte(copy.Display.PayloadUTF8), &object)
			object["proposal_basis"].(map[string]any)[path] = "other"
			data, _ := json.Marshal(object)
			var p reviewPackage
			_ = json.Unmarshal(data, &p)
			setReviewPayload(t, &copy, p)
			if ValidateSourceClaimReview(copy, "lab-status", doc, handoffExtractor(), records, h) == nil {
				t.Fatal("source material drift accepted")
			}
		})
	}
	for _, path := range []string{"quoted_text", "quoted_text_hash", "span_id", "extraction_view_id", "repository_snapshot_id"} {
		t.Run("ref_"+path, func(t *testing.T) {
			copy := r
			var p reviewPackage
			_ = json.Unmarshal([]byte(r.Display.PayloadUTF8), &p)
			data, _ := json.Marshal(p.ProposalBasis.SourceRefs[0])
			var ref map[string]any
			_ = json.Unmarshal(data, &ref)
			ref[path] = "other"
			data, _ = json.Marshal(ref)
			_ = json.Unmarshal(data, &p.ProposalBasis.SourceRefs[0])
			setReviewPayload(t, &copy, p)
			if ValidateSourceClaimReview(copy, "lab-status", doc, handoffExtractor(), records, h) == nil {
				t.Fatal("reference drift accepted")
			}
		})
	}
}

func TestReviewAdmissionDirectExactReplay(t *testing.T) {
	r, _, _, h := reviewFixture(t)
	a := fixtureAdmission(h)
	a.Replayed = true
	launcher, trace := reviewLauncher(t, "admit", r, a, nil, nil)
	got, err := AdmitSourceClaim(t.Context(), launcher, r, "來源明確支持候選的測試範圍。")
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("admit mismatch: %v", err)
	}
	assertReviewOnlyCall(t, trace, "admit_reviewed_source_claim")
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "decision_by") || strings.Contains(string(data), "get_source_claim_review\",\"arguments") {
		t.Fatal("writer request acquired caller identity or pending-only preflight")
	}
}

func TestReviewFailuresReturnZero(t *testing.T) {
	for _, mode := range []string{"extra-tool", "duplicate-tool", "schema", "tool-error", "alias", "unknown", "missing", "abnormal-eof"} {
		t.Run(mode, func(t *testing.T) {
			r, doc, records, h := reviewFixture(t)
			launcher, _ := reviewLauncher(t, mode, r, fixtureAdmission(h), nil, nil)
			got, err := ReviewSourceClaim(t.Context(), launcher, "lab-status", doc, handoffExtractor(), records, h)
			if err == nil || !reflect.DeepEqual(got, SourceClaimReview{}) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("failure result: %v", err)
			}
		})
	}
	for _, mode := range []string{"tool-error", "abnormal-eof", "wrong-admission"} {
		t.Run("admit_"+mode, func(t *testing.T) {
			r, _, _, h := reviewFixture(t)
			launcher, _ := reviewLauncher(t, mode, r, fixtureAdmission(h), nil, nil)
			got, err := AdmitSourceClaim(t.Context(), launcher, r, "synthetic-secret reason")
			if err == nil || !reflect.DeepEqual(got, ReviewedAdmission{}) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("failure result: %v", err)
			}
		})
	}
	for _, reason := range []string{"", " ", " reason", "reason ", "reason\x00", strings.Repeat("界", 667)} {
		t.Run(fmt.Sprintf("reason_%d", len(reason)), func(t *testing.T) {
			r, _, _, _ := reviewFixture(t)
			got, err := AdmitSourceClaim(t.Context(), "/does-not-exist", r, reason)
			if err == nil || !reflect.DeepEqual(got, ReviewedAdmission{}) {
				t.Fatal("invalid reason accepted")
			}
		})
	}
}

func TestReviewerSchemaClosedContract(t *testing.T) {
	for _, name := range []string{"get_source_claim_review", "admit_reviewed_source_claim", "record_reviewed_source_claim_disposition"} {
		schema := reviewerSchema(name)
		data, _ := json.Marshal(schema)
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(data, &raw)
		if !validReviewerSchema(name, raw) {
			t.Fatal("native schema rejected")
		}
		for _, key := range []string{"required", "additionalProperties", "properties"} {
			changed := map[string]json.RawMessage{}
			for k, v := range raw {
				changed[k] = v
			}
			delete(changed, key)
			if validReviewerSchema(name, changed) {
				t.Fatal("incomplete schema accepted")
			}
		}
	}
}

func TestReviewerKeepsOtherAuthorityUnavailable(t *testing.T) {
	for _, name := range []string{"submit_text_source", "submit_extractor_output", "get_evidence_record", "admit_pending_proposal", "record_pending_proposal_disposition"} {
		t.Run(name, func(t *testing.T) {
			r, _, _, h := reviewFixture(t)
			launcher, trace := reviewLauncher(t, "read", r, fixtureAdmission(h), nil, nil)
			c, err := startReviewer(t.Context(), launcher)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.initialize(); err != nil {
				t.Fatal(err)
			}
			if c.call(name, map[string]string{}, &struct{}{}) == nil {
				t.Fatal("foreign tool accepted")
			}
			data, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), `"method":"tools/call"`) {
				t.Fatal("foreign call reached child")
			}
			assertTransportJoined(t, c)
		})
	}
}

func TestReviewAdmissionRejectsBeforeLauncher(t *testing.T) {
	for _, mode := range []string{"subject", "payload", "reason", "nil-context"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _, h := reviewFixture(t)
			launcher, trace := reviewLauncher(t, "admit", r, fixtureAdmission(h), nil, nil)
			reason := "來源支持候選的明確範圍。"
			ctx := t.Context()
			switch mode {
			case "subject":
				r.Subject.ReviewSubject.ProposalOccurrenceID = "occ:wrong"
			case "payload":
				r.Display.PayloadUTF8 += " "
			case "reason":
				reason = " "
			case "nil-context":
				ctx = nil
			}
			got, err := AdmitSourceClaim(ctx, launcher, r, reason)
			if err == nil || !reflect.DeepEqual(got, ReviewedAdmission{}) {
				t.Fatal("invalid local approval accepted")
			}
			if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("launcher started before local approval validation")
			}
		})
	}
}

func reviewLauncher(t *testing.T, mode string, r SourceClaimReview, a ReviewedAdmission, proposal, canonical *pendingRecord) (string, string) {
	t.Helper()
	launcher, trace := transportLauncher(t, "review-workflow-"+mode)
	fixture := map[string]any{"review": r, "admission": a, "proposal": proposal, "canonical": canonical}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(trace), "review.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return launcher, trace
}
func assertReviewOnlyCall(t *testing.T, trace, name string) {
	t.Helper()
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &r) != nil {
			t.Fatal("bad trace")
		}
		if r.Method == "tools/call" {
			calls++
			if r.Params.Name != name {
				t.Fatal("unexpected tool")
			}
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func serveReviewHelper(mode string) int {
	tracePath := os.Getenv("DETECTIVE_TRANSPORT_MARKER")
	trace, err := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 2
	}
	defer trace.Close()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(tracePath), "review.json"))
	if err != nil {
		return 3
	}
	var fixture map[string]json.RawMessage
	if json.Unmarshal(data, &fixture) != nil {
		return 4
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), maxRPCBytes)
	query := mode == "query" || mode == "query-exit-once"
	failExit := false
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string                     `json:"name"`
				Arguments map[string]json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 5
		}
		if _, err := fmt.Fprintln(trace, string(scanner.Bytes())); err != nil {
			return 6
		}
		if request.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			kind := "valid"
			if query {
				kind = "query-server"
			}
			result = transportInitializeResult(kind)
		case "tools/list":
			if query {
				result = queryToolsFixture("valid")
				break
			}
			names := []string{"get_source_claim_review", "admit_reviewed_source_claim", "record_reviewed_source_claim_disposition"}
			if mode == "extra-tool" {
				names = append(names, "admit_pending_proposal")
			}
			if mode == "duplicate-tool" {
				names[1] = names[0]
			}
			tools := []map[string]any{}
			for _, name := range names {
				schema := reviewerSchema(name)
				if mode == "schema" {
					schema = map[string]any{"type": "object"}
				}
				tools = append(tools, map[string]any{"name": name, "inputSchema": schema})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			key := "review"
			if request.Params.Name == "admit_reviewed_source_claim" {
				key = "admission"
			}
			if request.Params.Name == "record_reviewed_source_claim_disposition" {
				key = "disposition"
			}
			if request.Params.Name == "get_evidence_record" {
				key = "proposal"
				if _, ok := request.Params.Arguments["canonical_id"]; ok {
					key = "canonical"
				}
			}
			// Faults affect the first tool call only, without changing the launcher
			// or fixture between attempts. This marker is not a simulated DB commit.
			if mode == "drop-write-once" || mode == "exit-once" || mode == "query-exit-once" {
				marker, err := os.OpenFile(filepath.Join(filepath.Dir(tracePath), "fault-fired"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err == nil {
					_, writeErr := marker.WriteString("request-received")
					closeErr := marker.Close()
					if writeErr != nil || closeErr != nil {
						return 11
					}
					if mode == "drop-write-once" {
						return 10
					}
					failExit = true
				} else if !errors.Is(err, os.ErrExist) {
					return 12
				}
			}
			payload := fixture[key]
			if mode == "admit" && key == "review" {
				return 7
			}
			if mode == "wrong-admission" {
				var a ReviewedAdmission
				_ = json.Unmarshal(payload, &a)
				a.AdmissionOutcome = "pending"
				payload, _ = json.Marshal(a)
			}
			if mode == "alias" {
				payload = []byte(strings.Replace(string(payload), "contract_version", "Contract_Version", 1))
			}
			if mode == "missing" {
				var obj map[string]any
				_ = json.Unmarshal(payload, &obj)
				delete(obj, "subject")
				payload, _ = json.Marshal(obj)
			}
			if mode == "unknown" {
				var obj map[string]any
				_ = json.Unmarshal(payload, &obj)
				obj["future_review_authority"] = "synthetic-secret"
				payload, _ = json.Marshal(obj)
			}
			if mode == "tool-error" {
				payload = []byte(`{"message":"synthetic-secret"}`)
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(payload)}}, "structuredContent": payload, "isError": mode == "tool-error"}
		default:
			return 8
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			return 9
		}
		if failExit && request.Method == "tools/call" {
			if err := os.WriteFile(filepath.Join(filepath.Dir(tracePath), "fault-fired"), []byte("response-written"), 0o600); err != nil {
				return 13
			}
		}
	}
	if mode == "abnormal-eof" || failExit {
		return 10
	}
	return 0
}
