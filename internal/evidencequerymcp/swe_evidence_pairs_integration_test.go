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

// TestIntegrationSWEEvidencePairs persists exact public Git excerpts for a
// paired-evidence experiment. Native dispatch validates source fidelity, not
// semantic sufficiency. Each record uses a disposable schema with no admission.
func TestIntegrationSWEEvidencePairs(t *testing.T) {
	root := os.Getenv("AHE_SWE_PAIR_ROOT")
	if root == "" {
		t.Skip("opt-in public SWE source packet fixture")
	}
	if os.Getenv("DATABASE_DSN") == "" {
		t.Fatal("an explicitly selected disposable database is required")
	}
	data, err := os.ReadFile(filepath.Join(root, "records.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []struct {
		ID       string `json:"id"`
		Repo     string `json:"repo"`
		Revision string `json:"revision"`
		Path     string `json:"path"`
		URL      string `json:"url"`
		Text     string `json:"content"`
		Hash     string `json:"sha256"`
	}
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatal("expected six frozen source excerpts")
	}
	for _, record := range records {
		t.Run(record.ID, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			digest := sha256.Sum256([]byte(record.Text))
			if record.Text == "" || hex.EncodeToString(digest[:]) != record.Hash {
				t.Fatal("frozen source content hash mismatch")
			}
			server, err := evidenceingestionmcp.NewServer(pool)
			if err != nil {
				t.Fatal(err)
			}
			request := evidenceingestionmcp.SubmitExternalSourceRequest{
				SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1,
				RequestID:     "swe-evidence-pairs-v2-" + record.ID,
				SourceSystem:  "github", SourceNamespace: record.Repo,
				ObjectType: "repository_source_excerpt", ObjectID: record.Path + ":" + record.ID,
				Revision:       record.Revision,
				SourceLocation: record.URL,
				Title:          record.ID, ContentFormat: evidenceingestion.ExternalSourceContentFormatMarkdown,
				ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
				Content:         record.Text, Coverage: evidenceingestion.ExternalSourceCoverageExactExcerpt,
				Limitations: []string{"Exact pinned Git line range only. Other source ranges, runtime observations and tests are excluded. Source persistence is not semantic verification."},
				CollectorID: "codex-swe-evidence-pairs-v2", ConnectorID: "offline-pinned-git",
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
			if input.RawContentHash != "sha256:"+record.Hash || len(input.Spans) == 0 {
				t.Fatalf("source hash mismatch or empty span catalog: hash=%s spans=%d", input.RawContentHash, len(input.Spans))
			}
			for _, span := range input.Spans {
				if span.StartByte < 0 || span.EndByte > len(input.RenderedText) || span.StartByte >= span.EndByte || input.RenderedText[span.StartByte:span.EndByte] != span.Text {
					t.Fatal("native span does not match rendered source")
				}
			}
			for name, content := range map[string][]byte{"request": payload, "receipt": receipt, "query": query, "view": view} {
				path := filepath.Join(root, "ahe-native", record.ID+"."+name+".json")
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
