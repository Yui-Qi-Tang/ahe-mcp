package ahemcp

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

func TestProposalStatementPreservesClassifiedContext(t *testing.T) {
	record := labstatus.Record{
		RecordType: "release_gate", Subject: "臺灣測試🙂", Statement: "The source reports an open lab gate, not a deployment.",
		EpistemicClass: "blocked", Status: "open", Scope: "lab_contract", SelectionState: "deferred",
		BlockedBy:        []string{"named prerequisite", "second prerequisite"},
		DoesNotEstablish: []string{"production operation"}, Qualifiers: []string{"quoted \"value\"\nnext line", "限定範圍"},
	}
	extractor := handoffExtractor()
	extractor.Version = "0.1.2"
	got, err := ProposalStatement(extractor, record)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "\n\nDetective extraction context (model-classified candidate, not independent verification):\n"
	body, ok := strings.CutPrefix(got, record.Statement+marker)
	if !ok {
		t.Fatal("projection changed the original statement or lost its classification boundary")
	}
	var context map[string]any
	if err := json.Unmarshal([]byte(body), &context); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"record_type": record.RecordType, "subject": record.Subject, "epistemic_class": record.EpistemicClass,
		"status": record.Status, "scope": record.Scope, "selection_state": record.SelectionState,
		"blocked_by":         []any{"named prerequisite", "second prerequisite"},
		"does_not_establish": []any{"production operation"}, "qualifiers": []any{"quoted \"value\"\nnext line", "限定範圍"},
	}
	if !reflect.DeepEqual(context, want) {
		t.Fatalf("classified context = %#v, want %#v", context, want)
	}
	if again, err := ProposalStatement(extractor, record); err != nil || again != got {
		t.Fatal("same version and record changed projection bytes", err)
	}
	for name, mutate := range map[string]func(*labstatus.Record){
		"type":               func(r *labstatus.Record) { r.RecordType = "capability_state" },
		"subject":            func(r *labstatus.Record) { r.Subject = "another subject" },
		"epistemic":          func(r *labstatus.Record) { r.EpistemicClass = "claim" },
		"status":             func(r *labstatus.Record) { r.Status = "lab_proven" },
		"scope":              func(r *labstatus.Record) { r.Scope = "runtime_core" },
		"selection":          func(r *labstatus.Record) { r.SelectionState = "selected" },
		"blocked_by":         func(r *labstatus.Record) { r.BlockedBy = []string{"different prerequisite"} },
		"does_not_establish": func(r *labstatus.Record) { r.DoesNotEstablish = []string{"different non-claim"} },
		"qualifiers":         func(r *labstatus.Record) { r.Qualifiers = []string{"different qualifier"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := record
			mutate(&changed)
			projection, err := ProposalStatement(extractor, changed)
			if err != nil || projection == got {
				t.Fatal("changed context was lost from the native statement", err)
			}
		})
	}
}

func TestProposalStatementLegacyNativeRequestStable(t *testing.T) {
	for version, wantRequest := range map[string]string{
		"0.1.0": "detective-v1-1f9032769f8618267bf7c6d7139d314239da82790f0d856e7f09f8256c9f9151",
		"0.1.1": "detective-v1-991d17d7d357496329ffd0cd45a8f7687cb95b73e0e8709bef27d6d796e02753",
	} {
		t.Run(version, func(t *testing.T) {
			doc, records := handoffDocument(t, "Legacy quoted source.\n")
			records[0].Subject, records[0].Statement = "Ordinary admission integrity", "Ordinary admission integrity"
			x := handoffExtractor()
			x.Version = version
			for _, statement := range []string{records[0].Statement, " \tLegacy phrase\r\n"} {
				legacy := records[0]
				legacy.Statement = statement
				if got, err := ProposalStatement(x, legacy); err != nil || got != statement {
					t.Fatal("legacy statement bytes were rewritten or requalified", err)
				}
			}
			launcher, trace := handoffLauncher(t, "valid")
			if _, err := Submit(t.Context(), launcher, "lab-status", doc, x, records); err != nil {
				t.Fatal(err)
			}
			var request extractorRequest
			if err := json.Unmarshal(readHandoffTrace(t, trace)[5].Arguments, &request); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(request.ExtractorOutput)
			const wantOutput = `{"proposals":[{"proposal_local_id":"record-1","statement_text":"Ordinary admission integrity","evidence_refs":["span:S1"]}]}`
			if err != nil || string(body) != wantOutput || request.RequestID != wantRequest {
				t.Fatalf("legacy output/request changed: %s %s %v", body, request.RequestID, err)
			}
		})
	}
}

func TestProposalStatementExactByteLimit(t *testing.T) {
	_, records := handoffDocument(t, "Synthetic source evidence.\n")
	x := handoffExtractor()
	x.Version = "0.1.2"
	records[0].Qualifiers = []string{"臺灣🙂"}
	base, err := ProposalStatement(x, records[0])
	if err != nil {
		t.Fatal(err)
	}
	records[0].Statement += strings.Repeat("x", 2000-len(base))
	got, err := ProposalStatement(x, records[0])
	if err != nil || len(got) != 2000 {
		t.Fatalf("exact byte limit rejected: bytes=%d err=%v", len(got), err)
	}
	records[0].Statement += "x"
	if got, err := ProposalStatement(x, records[0]); err == nil || got != "" {
		t.Fatal("overflow returned a truncated or successful projection")
	}
}

func TestProposalStatementRejectsBeforeLauncher(t *testing.T) {
	for _, name := range []string{"subject_only", "projected_overflow", "unknown_official_version"} {
		t.Run(name, func(t *testing.T) {
			doc, records := handoffDocument(t, "Synthetic source evidence.\n")
			x := handoffExtractor()
			x.Version = "0.1.2"
			switch name {
			case "subject_only":
				records[0].Statement = records[0].Subject
			case "projected_overflow":
				// Each input field fits its old bound; the complete projection does not.
				records[0].Qualifiers = []string{strings.Repeat("x", 1800)}
			case "unknown_official_version":
				x.Version = "0.1.99"
			}
			before, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			launcher, trace := handoffLauncher(t, "valid")
			if _, err := Submit(t.Context(), launcher, "lab-status", doc, x, records); err == nil {
				t.Fatal("invalid projected submission was accepted")
			}
			if err := VerifyPending(t.Context(), launcher, "lab-status", doc, x, records, queryHandoff()); err == nil {
				t.Fatal("invalid projection reached pending verification")
			}
			if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("projection rejection started a subprocess", err)
			}
			after, err := json.Marshal(records)
			if err != nil || string(after) != string(before) {
				t.Fatal("rejection mutated or truncated input", err)
			}
		})
	}
}

func TestProposalStatementExactSubmitQueryAndReview(t *testing.T) {
	review, doc, records, h := reviewFixture(t)
	x := handoffExtractor()
	x.Version = "0.1.2"
	records[0].Qualifiers = []string{"synthetic lab only"}
	want, err := ProposalStatement(x, records[0])
	if err != nil {
		t.Fatal(err)
	}
	launcher, trace := handoffLauncher(t, "valid")
	if _, err := Submit(t.Context(), launcher, "lab-status", doc, x, records); err != nil {
		t.Fatal(err)
	}
	var request extractorRequest
	if err := json.Unmarshal(readHandoffTrace(t, trace)[5].Arguments, &request); err != nil {
		t.Fatal(err)
	}
	if request.ExtractorOutput.Proposals[0].StatementText != want {
		t.Fatal("Submit did not use the complete versioned statement")
	}
	q := queryFixture(doc, records[0], h)
	q.StatementText, q.Extractor.Version = want, x.Version
	query, _ := reviewLauncher(t, "query", review, fixtureAdmission(h), &q, nil)
	if err := VerifyPending(t.Context(), query, "lab-status", doc, x, records, h); err != nil {
		t.Fatal("exact projected pending readback failed", err)
	}
	var pkg reviewPackage
	if err := json.Unmarshal([]byte(review.Display.PayloadUTF8), &pkg); err != nil {
		t.Fatal(err)
	}
	// Use the actual fake-process intake payload, not a second production projection.
	review.SubmissionReceipt.RequestID = request.RequestID
	review.SubmissionReceipt.ExtractorOutputHash = "sha256:" + digest(request.ExtractorOutput)
	review.ProposalManifest.ExtractorOutputHash = review.SubmissionReceipt.ExtractorOutputHash
	pkg.ProposalBasis.StatementText, pkg.ProposalBasis.ExtractorVersion = want, x.Version
	setReviewPayload(t, &review, pkg)
	if err := ValidateSourceClaimReview(review, "lab-status", doc, x, records, h); err != nil {
		t.Fatal("review request/output hashes differ from actual intake", err)
	}
	reviewer, _ := reviewLauncher(t, "read", review, fixtureAdmission(h), nil, nil)
	if got, err := ReviewSourceClaim(t.Context(), reviewer, "lab-status", doc, x, records, h); err != nil || !reflect.DeepEqual(got, review) {
		t.Fatal("review process did not preserve the exact projected display", err)
	}
	for _, name := range []string{"raw_statement", "request_id", "output_hash", "changed_context"} {
		t.Run(name, func(t *testing.T) {
			changed, display := review, pkg
			candidate := records[0]
			switch name {
			case "raw_statement":
				display.ProposalBasis.StatementText = candidate.Statement
				setReviewPayload(t, &changed, display)
			case "request_id":
				changed.SubmissionReceipt.RequestID = "detective-v1-legacy-request"
			case "output_hash":
				changed.SubmissionReceipt.ExtractorOutputHash = "sha256:" + strings.Repeat("b", 64)
				changed.ProposalManifest.ExtractorOutputHash = changed.SubmissionReceipt.ExtractorOutputHash
			case "changed_context":
				candidate.Qualifiers = []string{"different scope qualifier"}
			}
			if ValidateSourceClaimReview(changed, "lab-status", doc, x, []labstatus.Record{candidate}, h) == nil {
				t.Fatal("review accepted a stale or incomplete native projection")
			}
		})
	}
	q.StatementText = records[0].Statement
	if verifyPendingRecord(q, "lab-status", doc, x, records[0], h) == nil {
		t.Fatal("Query accepted the old raw projection for a new extractor")
	}
}

func TestProposalStatementContextChangesNativeRequest(t *testing.T) {
	doc, records := handoffDocument(t, "Synthetic source evidence.\n")
	x := handoffExtractor()
	x.Version = "0.1.2"
	var first extractorRequest
	for _, qualifier := range []string{"scope A", "scope B", "scope A"} {
		records[0].Qualifiers = []string{qualifier}
		launcher, trace := handoffLauncher(t, "valid")
		if _, err := Submit(t.Context(), launcher, "lab-status", doc, x, records); err != nil {
			t.Fatal(err)
		}
		var got extractorRequest
		if err := json.Unmarshal(readHandoffTrace(t, trace)[5].Arguments, &got); err != nil {
			t.Fatal(err)
		}
		if first.RequestID == "" {
			first = got
			continue
		}
		if (got.RequestID == first.RequestID) != (qualifier == "scope A") {
			t.Fatal("native request failed to distinguish changed context or exact retry")
		}
	}
}
