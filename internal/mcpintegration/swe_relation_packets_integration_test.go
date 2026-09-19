//go:build integration

package mcpintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sixCaseRecord struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Hash    string `json:"sha256"`
	Start   int    `json:"start_line"`
	End     int    `json:"end_line"`
}
type sixCaseLink struct {
	From string `json:"from_record"`
	To   string `json:"to_record"`
	Name string `json:"name"`
	Rule string `json:"rule"`
}
type sixCaseSource struct {
	ID      string          `json:"id"`
	Repo    string          `json:"repo"`
	Commit  string          `json:"base_commit"`
	Records []sixCaseRecord `json:"records"`
	Links   []sixCaseLink   `json:"candidate_links"`
}
type sixCaseTrace struct {
	Tool      string          `json:"tool"`
	Arguments any             `json:"arguments"`
	Result    json.RawMessage `json:"result"`
}

func sixCaseCall[T any](t *testing.T, p *authorityProcess, trace *[]sixCaseTrace, name string, args any) T {
	t.Helper()
	result := authorityProcessTool[T](t, p, name, args)
	*trace = append(*trace, sixCaseTrace{name, args, slices.Clone(p.lastToolContent)})
	return result
}

// TestIntegrationSWERelationPackets runs actual standard MCP profiles in a
// disposable DB. Public source bytes are real. Reviews are APPROVAL STUBS for
// syntax-candidate provenance only, not real human approval or causal truth.
func TestIntegrationSWERelationPackets(t *testing.T) {
	root := os.Getenv("AHE_SIX_CASE_ROOT")
	if root == "" {
		t.Skip("opt-in six-case public-source relation experiment")
	}
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Fatal("explicit disposable database required")
	}
	raw, err := os.ReadFile(filepath.Join(root, "swe-inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []sixCaseSource
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 {
		t.Fatal("expected three public cases")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	binaryDirectory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binaries := buildProvisioningCommands(t, ctx, binaryDirectory)
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileEndpointReviewer)
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			start := func(login authorityProcessLogin, profile, repositoryRoot string) *authorityProcess {
				config, err := pgxpool.ParseConfig(login.dsn)
				if err != nil {
					t.Fatal("invalid synthetic runtime configuration")
				}
				credential := provisioningExplicitURL(config, config.ConnConfig.Database, config.ConnConfig.User, config.ConnConfig.Password)
				credentialPath := filepath.Join(directory, profile+".dsn")
				writeProvisioningProtectedFile(t, credentialPath, []byte(credential))
				binary := "ahe-ingest-mcp"
				if profile == "query" {
					binary = "ahe-query-mcp"
				}
				fields := map[string]string{"schema_version": "ahe-mcp-launcher/v1", "binary_path": binaries[binary],
					"database_dns_file": credentialPath, "database": config.ConnConfig.Database, "session_user": config.ConnConfig.User,
					"schema": f.schema, "role": login.group, "profile": profile, "principal_id": "mock:protected:" + profile}
				if profile == "repository-intake" {
					fields["repository_root"] = repositoryRoot
					fields["repository_id"] = "synthetic:refund"
				}
				payload, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(directory, profile+".json")
				writeProvisioningProtectedFile(t, path, payload)
				return startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], path, binary)
			}

			intake := start(f.intake, "intake", "")
			reviewer := start(f.reviewer, "source-claim-reviewer", "")
			endpoints := start(f.endpoints, "endpoint-reviewer", "")
			query := start(f.query, "query", "")
			var trace []sixCaseTrace
			tools := map[string]json.RawMessage{}
			for name, p := range map[string]*authorityProcess{"intake": intake, "reviewer": reviewer, "endpoints": endpoints, "query": query} {
				response := p.request(t, "tools/list", map[string]any{})
				if response.Error != nil {
					t.Fatal("schema discovery failed")
				}
				tools[name] = response.Result
			}
			sourceByID := map[string]evidenceingestionmcp.SubmitExternalSourceResponse{}
			canonicalByID := map[string]string{}
			aliasByCanonical := map[string]string{}
			for _, r := range c.Records {
				digest := sha256.Sum256([]byte(r.Content))
				if hex.EncodeToString(digest[:]) != r.Hash {
					t.Fatal("source digest changed")
				}
				system, namespace, objectType, objectID, revision, location := "github", c.Repo, "repository_source_excerpt", fmt.Sprintf("%s:%d-%d", r.Path, r.Start, r.End), c.Commit, "https://github.com/"+c.Repo+"/blob/"+c.Commit+"/"+r.Path
				if r.ID == "issue" {
					system = "huggingface"
					namespace = "princeton-nlp/SWE-bench_Lite"
					objectType = "benchmark_problem_statement"
					objectID = c.ID
					revision = "6ec7bb89b9342f664a54a6e0a6ea6501d3437cc2"
					location = "https://huggingface.co/datasets/princeton-nlp/SWE-bench_Lite"
				}
				source := sixCaseCall[evidenceingestionmcp.SubmitExternalSourceResponse](t, intake, &trace, "submit_external_source", evidenceingestion.ExternalSourceEnvelopeV1{
					SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: c.ID + "-" + r.ID,
					SourceSystem: system, SourceNamespace: namespace, ObjectType: objectType, ObjectID: objectID, Revision: revision, SourceLocation: location, Title: c.ID + " " + r.ID,
					ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
					Content: r.Content, Coverage: evidenceingestion.ExternalSourceCoverageExactExcerpt,
					Limitations: []string{"Only the stated exact public issue field or pinned Git line range. Other source and runtime evidence excluded."},
					CollectorID: "six-case-public-git-adapter", ConnectorID: "offline-pinned-git-or-official-arrow", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)})
				sourceByID[r.ID] = source
				refs := []string{}
				for _, span := range source.Spans {
					refs = append(refs, span.SpanID)
				}
				proposal := sixCaseCall[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, &trace, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
					RequestID: c.ID + "-extract-" + r.ID, SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
					ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "six-case-public-verbatim-fixture", Version: "v3"},
					ExtractorOutput:     evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "source", StatementText: r.Content, EvidenceRefs: refs}}}})
				review := sixCaseCall[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, &trace, "get_source_claim_review", evidenceingestionmcp.GetSourceClaimReviewRequest{ExtractionAttemptID: proposal.ExtractionAttemptID, ProposalOccurrenceID: proposal.ProposalOccurrenceID})
				admitted := sixCaseCall[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, &trace, "admit_reviewed_source_claim", evidenceingestionmcp.AdmitReviewedSourceClaimRequest{
					ExtractionAttemptID: proposal.ExtractionAttemptID, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "APPROVAL STUB: public verbatim excerpt in disposable experiment only; not human approval, semantic truth or complete coverage."})
				canonicalByID[r.ID] = admitted.CanonicalRef
				aliasByCanonical[admitted.CanonicalRef] = r.ID
			}
			derivedAliases := map[string]string{}
			for i, link := range c.Links {
				source := sourceByID[link.From]
				refs := []string{}
				for _, span := range source.Spans {
					refs = append(refs, span.SpanID)
				}
				sentence := fmt.Sprintf("The identifier spelling %q occurs in excerpt %s; the external Python AST adapter selected excerpt %s as its unique same-file declaration candidate. This is syntactic navigation context, not runtime resolution or a confirmed diagnosis.", link.Name, link.From, link.To)
				proposal := sixCaseCall[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, &trace, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
					RequestID: fmt.Sprintf("%s-link-%d", c.ID, i), SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
					ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "six-case-python-syntax-candidate", Version: "v3"},
					ExtractorOutput:     evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "candidate", StatementText: sentence, EvidenceRefs: refs}}}})
				parents := []string{canonicalByID[link.From], canonicalByID[link.To]}
				slices.Sort(parents)
				request := evidenceingestion.EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: proposal.ProposalOccurrenceID, Derivation: &evidenceingestion.EndpointDerivation{
					ParentNodeIDs: parents, Method: link.Rule, Producer: "six-case-python-ast-adapter", TraceRef: fmt.Sprintf("public-syntax-link:%s:%d", c.ID, i)}}
				review := sixCaseCall[evidenceingestion.EndpointReview](t, endpoints, &trace, mcpendpoints.ToolReview, request)
				if len(review.Display.SourceLeaves) != 2 {
					t.Fatal("candidate must expose both exact source leaves")
				}
				receipt := sixCaseCall[evidenceingestion.EndpointAdmissionReceipt](t, endpoints, &trace, mcpendpoints.ToolAdmit, evidenceingestion.ReviewedEndpointAdmissionInput{
					RequestID: fmt.Sprintf("%s-link-admit-%d", c.ID, i), Review: request, ExpectedSubject: review.Subject, Decision: "approved",
					DecisionReason: "APPROVAL STUB: accept only the declared two-source syntax-candidate provenance for a disposable experiment. No real human approval, runtime call resolution, diagnosis, or fix verification."})
				derivedAliases[receipt.Admission.CanonicalRef] = fmt.Sprintf("link-%d", i)
			}
			// The extraction controller actually follows derived_from from each seed
			// to the candidate assertion and back to its complete source basis.
			before := relationCounts(t, ctx, f.pool)
			queryStart := len(trace)
			seen := map[string]bool{}
			selected := map[string]bool{"issue": true}
			edges := map[string]any{}
			queue := []string{}
			for id, node := range canonicalByID {
				if strings.HasPrefix(id, "source-") {
					queue = append(queue, node)
				}
			}
			slices.Sort(queue)
			for len(queue) > 0 {
				current := queue[0]
				queue = queue[1:]
				if seen[current] {
					continue
				}
				seen[current] = true
				neighbors := sixCaseCall[evidencequerymcp.ListEvidenceNeighborsResponse](t, query, &trace, "list_evidence_neighbors", evidencequerymcp.ListEvidenceNeighborsRequest{CanonicalID: current, Direction: "outgoing", Relation: "derived_from", Limit: 100})
				if neighbors.Count >= 100 {
					t.Fatal("unresolved neighbor pagination")
				}
				for _, n := range neighbors.Neighbors {
					if n.AdjacentCanonical == nil {
						t.Fatal("missing candidate endpoint")
					}
					// RecordRef IDs are not assumed: the exact edge supplies the endpoint.
					edge := n.Relation.CanonicalEdge
					if edge == nil {
						t.Fatal("missing canonical edge")
					}
					derived := edge.To.ID
					basis := sixCaseCall[evidencequerymcp.ListEvidenceNeighborsResponse](t, query, &trace, "list_evidence_neighbors", evidencequerymcp.ListEvidenceNeighborsRequest{CanonicalID: derived, Direction: "incoming", Relation: "derived_from", Limit: 100})
					if basis.Count != 2 {
						t.Fatal("candidate lost its complete two-parent basis")
					}
					for _, parent := range basis.Neighbors {
						e := parent.Relation.CanonicalEdge
						if e == nil {
							t.Fatal("missing parent edge")
						}
						alias, ok := aliasByCanonical[e.From.ID]
						if !ok {
							t.Fatal("unmapped source parent")
						}
						selected[alias] = true
						queue = append(queue, e.From.ID)
						edges[parent.Relation.RelationRef.ID] = e
					}
				}
			}
			for id := range canonicalByID {
				if strings.HasPrefix(id, "source-") {
					selected[id] = true
				}
			}
			views := map[string]evidenceingestionmcp.GetExtractorInputResponse{}
			for _, r := range c.Records {
				if selected[r.ID] {
					view := sixCaseCall[evidenceingestionmcp.GetExtractorInputResponse](t, intake, &trace, "get_extractor_input", evidenceingestionmcp.GetExtractorInputRequest{ExtractionViewID: sourceByID[r.ID].ExtractionViewID})
					if view.RenderedText != r.Content {
						t.Fatal("relation-selected source bytes changed")
					}
					views[r.ID] = view
				}
			}
			if len(selected) != len(c.Records) || len(edges) != 2*len(c.Links) {
				t.Fatal("relation traversal did not recover the frozen candidate source pool")
			}
			if relationCounts(t, ctx, f.pool) != before {
				t.Fatal("relation lookup wrote authority")
			}
			packet := map[string]any{"case": c.ID, "synthetic_approvals": true, "real_stdio_mcp": true, "tools": tools, "trace": trace, "query_trace_start": queryStart, "canonical_aliases": canonicalByID, "derived_aliases": derivedAliases, "queried_edges": edges, "selected_records": selected, "selected_views": views, "read_only_counts_unchanged": true, "limits": "External Python AST navigation candidates with simulated review. AHE verifies declared provenance and reads the persisted graph; neither derives a causal diagnosis nor verifies a patch."}
			data, err := json.MarshalIndent(packet, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, c.ID+"-native-graph.json")
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatal("cannot export graph trace")
			}
		})
	}
}
