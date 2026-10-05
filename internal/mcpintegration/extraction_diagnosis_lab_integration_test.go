//go:build integration && diagnosislab

package mcpintegration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

// TestIntegrationExtractionDiagnosisReadback records deliberately fallible
// synthetic extractions. TEST APPROVAL STUB is not a semantic fidelity verdict.
// Intake/query use actual stdio MCP; exact-reviewed admission uses the native API.
func TestIntegrationExtractionDiagnosisReadback(t *testing.T) {
	directory := os.Getenv("AHE_DIAGNOSIS_LAB_DIR")
	if directory == "" || os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN") == "" {
		t.Fatal("explicit diagnosis fixture directory and disposable database are required")
	}
	body, err := os.ReadFile(filepath.Join(directory, "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID      string `json:"id"`
		Records []struct {
			ClaimID    string `json:"claim_id"`
			SourceID   string `json:"source_id"`
			Statement  string `json:"statement"`
			SourceText string `json:"source_text"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 14 {
		t.Fatal("frozen fixture denominator changed")
	}
	s := newCoreLabSession(t)
	defer s.finish(t)
	count := 0
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			var results []map[string]any
			for _, record := range c.Records {
				id := "diagnosis-" + c.ID + "-" + record.ClaimID
				subject := s.subject(t, id, record.SourceText, record.Statement)
				pending := sixCaseCall[evidencequerymcp.GetEvidenceRecordResponse](t, s.query, &s.trace,
					"get_evidence_record", map[string]any{"proposal_occurrence_id": subject.ProposalOccurrenceID})
				display, expected, err := evidenceingestion.LoadSourceClaimReviewDisplayArtifact(s.ctx, s.fixture.pool,
					pending.Extractor.ExtractionAttemptID, subject.ProposalOccurrenceID)
				if err != nil {
					t.Fatal(err)
				}
				input := evidenceingestion.ReviewedSourceClaimAdmissionInput{
					ExtractionAttemptID: pending.Extractor.ExtractionAttemptID,
					ExpectedSubject:     expected,
					DecisionBy:          "TEST APPROVAL STUB",
					DecisionReason:      "Synthetic fallible extraction experiment; not human review or proof of fidelity",
				}
				admission, err := evidenceingestion.AdmitReviewedSourceClaim(s.ctx, s.fixture.pool, input)
				if err != nil {
					t.Fatal(err)
				}
				got := sixCaseCall[evidencequerymcp.GetEvidenceRecordResponse](t, s.query, &s.trace,
					"get_evidence_record", map[string]any{"proposal_occurrence_id": subject.ProposalOccurrenceID})
				if got.StatementText != record.Statement || got.AdmissionOutcome != "admitted" ||
					got.CanonicalRef == nil || *got.CanonicalRef != admission.CanonicalRef ||
					got.Source.SourceSnapshotID != subject.SourceSnapshotID ||
					got.Source.RawContentHash != stdioContentHash([]byte(record.SourceText)) ||
					len(got.SourceRefs) != 1 || got.SourceRefs[0].QuotedText != record.SourceText {
					t.Fatal("claim, exact source, admission or source identity changed during readback")
				}
				if got.Extractor.ExtractionAttemptID != pending.Extractor.ExtractionAttemptID ||
					got.SourceRefs[0].StartByte != 0 || got.SourceRefs[0].EndByte != len(record.SourceText) ||
					got.SourceRefs[0].QuotedTextHash != stdioContentHash([]byte(record.SourceText)) {
					t.Fatal("source span boundaries, hash or extraction attempt changed")
				}
				results = append(results, map[string]any{
					"claim_id": record.ClaimID, "source_id": record.SourceID,
					"pending_readback": pending, "review_display": display, "admission_input": input,
					"admission_receipt": admission, "admitted_readback": got,
				})
				count++
			}
			output, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "native", c.ID+".json"), output, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
	if count != 30 {
		t.Fatalf("read back %d claims, want 30", count)
	}
}
