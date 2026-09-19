//go:build integration

package evidencequerymcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
)

// TestIntegrationSWESourcePackets exports source-only packets from public frozen
// issues. It exercises native tool dispatch, not a transport session, and creates
// no proposals, approvals or canonical evidence. Each case uses a disposable schema.
func TestIntegrationSWESourcePackets(t *testing.T) {
	root := os.Getenv("AHE_SWE_PACKET_ROOT")
	if root == "" {
		t.Skip("opt-in public SWE source packet fixture")
	}
	if os.Getenv("DATABASE_DSN") == "" {
		t.Fatal("an explicitly selected disposable database is required")
	}
	for _, id := range []string{"django__django-10914", "astropy__astropy-12907", "scikit-learn__scikit-learn-25570"} {
		t.Run(id, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			data, err := os.ReadFile(filepath.Join(root, "model-input", id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var issue struct {
				Text string `json:"problem_statement"`
				Hash string `json:"issue_sha256"`
			}
			if err := json.Unmarshal(data, &issue); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(issue.Text))
			if issue.Text == "" || hex.EncodeToString(digest[:]) != issue.Hash {
				t.Fatal("frozen issue content hash mismatch")
			}
			server, err := evidenceingestionmcp.NewServer(pool)
			if err != nil {
				t.Fatal(err)
			}
			request := evidenceingestionmcp.SubmitExternalSourceRequest{
				SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1,
				RequestID:     "swe-public-v1-" + id,
				SourceSystem:  "huggingface", SourceNamespace: "princeton-nlp/SWE-bench_Lite",
				ObjectType: "benchmark_problem_statement", ObjectID: id,
				Revision:       "6ec7bb89b9342f664a54a6e0a6ea6501d3437cc2",
				SourceLocation: "https://huggingface.co/datasets/princeton-nlp/SWE-bench_Lite",
				Title:          id, ContentFormat: evidenceingestion.ExternalSourceContentFormatMarkdown,
				ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
				Content:         issue.Text, Coverage: evidenceingestion.ExternalSourceCoverageExactExcerpt,
				Limitations: []string{"Only the official problem_statement field; evaluator fields and upstream issue comments are excluded."},
				CollectorID: "codex-swe-public-v1", ConnectorID: "offline-arrow-cache",
				ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			payload, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := server.CallTool(ctx, evidenceingestionmcp.ToolSubmitExternalSource, payload)
			if err != nil {
				t.Fatal(err)
			}
			var source evidenceingestionmcp.SubmitExternalSourceResponse
			if err := json.Unmarshal(receipt, &source); err != nil {
				t.Fatal(err)
			}
			query, err := json.Marshal(evidenceingestionmcp.GetExtractorInputRequest{ExtractionViewID: source.ExtractionViewID})
			if err != nil {
				t.Fatal(err)
			}
			before := tableCounts(t, ctx, pool)
			view, err := server.CallTool(ctx, evidenceingestionmcp.ToolGetExtractorInput, query)
			if err != nil {
				t.Fatal(err)
			}
			if before != tableCounts(t, ctx, pool) || before.proposalOccurrences != 0 || before.canonicalGraphNodes != 0 {
				t.Fatal("source-only read must not create proposals or authority records")
			}
			var input evidenceingestion.ExtractorInput
			if err := json.Unmarshal(view, &input); err != nil {
				t.Fatal(err)
			}
			if input.RawContentHash != "sha256:"+issue.Hash || len(input.Spans) == 0 {
				t.Fatalf("source hash mismatch or empty span catalog: hash=%s spans=%d", input.RawContentHash, len(input.Spans))
			}
			for _, span := range input.Spans {
				if span.StartByte < 0 || span.EndByte > len(input.RenderedText) || span.StartByte >= span.EndByte || input.RenderedText[span.StartByte:span.EndByte] != span.Text {
					t.Fatal("native span does not match rendered source")
				}
			}
			for name, content := range map[string][]byte{"request": payload, "receipt": receipt, "query": query, "view": view} {
				path := filepath.Join(root, "ahe-native", id+"."+name+".json")
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := file.Write(content)
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("export packet: write=%v close=%v", writeErr, closeErr)
				}
			}
		})
	}
}
