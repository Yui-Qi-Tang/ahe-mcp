//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

// This fixture uses real binaries, provisioned roles and stdio MCP. Every
// admission in this file is an explicit synthetic TEST APPROVAL STUB.
type coreLabSession struct {
	ctx                     context.Context
	fixture                 provisioningLauncherFixture
	intake, query, recorder *authorityProcess
	trace                   []sixCaseTrace
}

func newCoreLabSession(t *testing.T) *coreLabSession {
	t.Helper()
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	t.Cleanup(cancel)
	directory := provisioningProtectedDirectory(t, dsn)
	binaries := buildProvisioningCommands(t, ctx, directory)
	s := &coreLabSession{ctx: ctx, fixture: newProvisioningLauncherFixture(t, ctx, dsn, binaries["ahe-runtime-admin"])}
	for _, profile := range []dbrole.Profile{dbrole.ProfileIntake, dbrole.ProfileQuery, dbrole.ProfileCoreRecords} {
		identity := s.fixture.identities[profile]
		command := "ahe-ingest-mcp"
		if profile == dbrole.ProfileQuery {
			command = "ahe-query-mcp"
		}
		credential := filepath.Join(directory, string(profile)+".dsn")
		writeProvisioningProtectedFile(t, credential, []byte(identity.dsn))
		config := filepath.Join(directory, string(profile)+".json")
		writeProvisioningConfig(t, config, provisioningLauncherConfig{
			SchemaVersion: "ahe-mcp-launcher/v1", BinaryPath: binaries[command], DatabaseDNSFile: credential,
			Database: s.fixture.database, SessionUser: identity.login, Schema: s.fixture.schema,
			Role: identity.group, Profile: string(profile), PrincipalID: "mock:core-lab:" + string(profile),
		})
		process := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], config, command)
		switch profile {
		case dbrole.ProfileIntake:
			s.intake = process
		case dbrole.ProfileQuery:
			s.query = process
		case dbrole.ProfileCoreRecords:
			s.recorder = process
		}
	}
	t.Cleanup(func() {
		if directory := os.Getenv("AHE_CORE_LAB_REPORT_DIR"); directory != "" {
			body, err := json.MarshalIndent(s.trace, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(filepath.Join(directory, t.Name()+".json"), body, 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	return s
}

func (s *coreLabSession) finish(t *testing.T) {
	t.Helper()
	s.recorder.finish(t)
	s.query.finish(t)
	s.intake.finish(t)
}

func (s *coreLabSession) subject(t *testing.T, id, source, statement string) evidenceingestion.ExternalCheckSubject {
	t.Helper()
	r := sixCaseCall[evidenceingestionmcp.SubmitTextSourceResponse](t, s.intake, &s.trace, "submit_text_source", map[string]any{
		"request_id": id + "-source", "source_id": "mock:core-lab:" + id, "source_version": "1", "raw_text": source,
	})
	x := sixCaseCall[evidenceingestionmcp.GetExtractorInputResponse](t, s.intake, &s.trace, "get_extractor_input", map[string]any{"extraction_view_id": r.ExtractionViewID})
	if x.RenderedText != source || x.RawContentHash != stdioContentHash([]byte(source)) || len(x.Spans) == 0 {
		t.Fatal("source bytes or hash lost")
	}
	p := sixCaseCall[evidenceingestionmcp.SubmitExtractorOutputResponse](t, s.intake, &s.trace, "submit_extractor_output", map[string]any{
		"request_id": id + "-candidate", "source_snapshot_id": r.SourceSnapshotID, "extraction_view_id": r.ExtractionViewID,
		"extractor_definition": map[string]any{"name": "frozen-lab-candidate", "version": "1", "config": map[string]string{}},
		"extractor_output":     map[string]any{"proposals": []any{map[string]any{"proposal_local_id": "claim", "statement_text": statement, "evidence_refs": []string{x.Spans[0].SpanID}}}},
	})
	if p.Status != "pending" {
		t.Fatal("candidate bypassed pending")
	}
	return sixCaseCall[evidenceingestion.ExternalCheckSubject](t, s.query, &s.trace, "get_external_check_subject", map[string]any{"proposal_occurrence_id": p.ProposalOccurrenceID})
}

func coreLabRepresentation(id, role, content string, subject evidenceingestion.ExternalCheckSubject, dependencies []evidenceingestion.ExternalDependency) map[string]any {
	return map[string]any{"contract": "external-representation/v1", "request_id": id, "subject": subject, "name": "frozen-lab", "version": "1", "role": role, "format": "lab-ir", "format_version": "1", "content": content, "producer": "external synthetic fixture", "mapping_claim": "Opaque external declaration; source fidelity is not established", "dependencies": dependencies}
}

func coreLabCheck(id string, subject evidenceingestion.ExternalCheckSubject, materials []evidenceingestion.ExternalCheckMaterial, findings []evidenceingestion.ExternalCheckFinding) map[string]any {
	return map[string]any{"contract": "external-check/v1", "request_id": id, "subject": subject, "checker_name": "synthetic-recording-witness", "checker_version": "1", "checker_configuration": "storage-only; no semantic evaluator", "run_ref": "mock:core-lab:" + id, "materials": materials, "findings": findings, "limitations": []string{"Synthetic external assertions are not semantic proof or admission authority"}}
}

func coreLabExpectedRecord[T any](t *testing.T, arguments map[string]any) T {
	t.Helper()
	want := make(map[string]any, len(arguments)+1)
	for key, value := range arguments {
		want[key] = value
	}
	want["recorded_by"] = "mock:core-lab:core-records"
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// These are the 17 boundary and 24 stress candidates from the Lab. Their
// historical fidelity labels are retained but are NOT a Core pass/fail oracle.
func TestIntegrationCoreLabSemanticMaterialsMCP(t *testing.T) {
	body, err := os.ReadFile("testdata/core_lab_semantic_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Cases []struct {
			ID, Source, Statement string
			SourceIR              json.RawMessage `json:"source_ir"`
			CandidateIR           json.RawMessage `json:"candidate_ir"`
			AuthoredFidelity      bool            `json:"authored_fidelity"`
		}
	}
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Cases) != 41 {
		t.Fatal("frozen Lab denominator changed")
	}
	s := newCoreLabSession(t)
	defer s.finish(t)
	for _, c := range input.Cases {
		t.Run(c.ID, func(t *testing.T) {
			subject := s.subject(t, c.ID, c.Source, c.Statement)
			materials := []evidenceingestion.ExternalCheckMaterial{
				{ID: "source-text", Role: "source", Format: "verbatim", Version: "1", Content: c.Source, MappingClaim: "Exact source snapshot"},
				{ID: "candidate-text", Role: "candidate", Format: "verbatim", Version: "1", Content: c.Statement, MappingClaim: "Exact candidate statement"},
			}
			var representations []evidenceingestion.ExternalRepresentationRecord
			for _, v := range []struct{ role, content string }{{"source", string(c.SourceIR)}, {"candidate", string(c.CandidateIR)}} {
				a := coreLabRepresentation(c.ID+"-"+v.role, v.role, v.content, subject, []evidenceingestion.ExternalDependency{})
				r := sixCaseCall[evidenceingestion.ExternalRepresentationReceipt](t, s.recorder, &s.trace, "record_external_representation", a)
				got := sixCaseCall[evidenceingestion.ExternalRepresentationRecord](t, s.query, &s.trace, "get_external_representation", map[string]any{"representation_id": r.Record.ID})
				if got.Definition.Content != v.content || got.Definition.Subject != subject || !reflect.DeepEqual(got, r.Record) || !reflect.DeepEqual(got.Definition, coreLabExpectedRecord[evidenceingestion.ExternalRepresentationInput](t, a)) {
					t.Fatal("external IR or subject changed")
				}
				representations = append(representations, got)
				materials = append(materials, sixCaseCall[evidenceingestion.ExternalCheckMaterial](t, s.query, &s.trace, "get_external_representation_material", map[string]any{"representation_id": got.ID, "input_id": v.role + "-ir"}))
			}
			findings := []evidenceingestion.ExternalCheckFinding{
				{Dimension: "formal", Criterion: "Synthetic external PASS; tests preservation only", InputIDs: []string{"source-ir", "candidate-ir"}, Result: "pass", Detail: "No formal solver runs in this test"},
				{Dimension: "source-fidelity", Criterion: "Source and candidate semantic equivalence", InputIDs: []string{"source-text", "candidate-text"}, Result: "not_checked", Detail: "Historical authored labels are not re-evaluated by Core"},
				{Dimension: "causal-truth", Criterion: "Real world causal truth", Result: "unsupported", Detail: "Outside Core contract"},
			}
			a := coreLabCheck(c.ID+"-check", subject, materials, findings)
			r := sixCaseCall[evidenceingestion.ExternalCheckReceipt](t, s.recorder, &s.trace, "record_external_check", a)
			for _, repr := range representations {
				sixCaseCall[json.RawMessage](t, s.recorder, &s.trace, "link_external_check_representation", evidenceingestion.ExternalRepresentationLink{CheckID: r.Record.ID, InputID: repr.Definition.Role + "-ir", RepresentationID: repr.ID})
			}
			replay := sixCaseCall[evidenceingestion.ExternalCheckReceipt](t, s.recorder, &s.trace, "record_external_check", a)
			got := sixCaseCall[evidenceingestion.ExternalCheckRecord](t, s.query, &s.trace, "get_external_check", map[string]any{"check_id": r.Record.ID})
			if !replay.Replayed || !reflect.DeepEqual(replay.Record, r.Record) || !reflect.DeepEqual(got, r.Record) || !reflect.DeepEqual(got.Report, coreLabExpectedRecord[evidenceingestion.ExternalCheckInput](t, a)) {
				t.Fatal("check inputs, dimensions or historical receipt changed")
			}
			pending := sixCaseCall[evidencequerymcp.GetEvidenceRecordResponse](t, s.query, &s.trace, "get_evidence_record", map[string]any{"proposal_occurrence_id": subject.ProposalOccurrenceID})
			if pending.StatementText != c.Statement || pending.AdmissionOutcome != "pending" || pending.CanonicalRef != nil {
				t.Fatal("external PASS admitted or changed candidate")
			}
		})
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions"} {
		stdioAssertTableCount(t, s.ctx, s.fixture.pool, table, 0)
	}
}

func (s *coreLabSession) claim(t *testing.T, id string, parents ...string) string {
	t.Helper()
	subject := s.subject(t, id, "Synthetic evidence "+id, "Synthetic evidence "+id)
	in := evidenceingestion.AdmissionInput{ProposalOccurrenceID: subject.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "Synthetic graph fixture, not human semantic approval"}
	if len(parents) > 0 {
		in.Derivation = &evidenceingestion.DerivationAdmissionInput{ParentNodeIDs: parents, Method: "declared-rule", Producer: "synthetic fixture", TraceRef: "mock:" + id}
	}
	r, err := evidenceingestion.AdmitPendingProposal(s.ctx, s.fixture.pool, in)
	if err != nil {
		t.Fatal(err)
	}
	return r.CanonicalRef
}

func (s *coreLabSession) rejected(t *testing.T, tool string, args any) {
	t.Helper()
	r := s.recorder.request(t, "tools/call", map[string]any{"name": tool, "arguments": args})
	var result struct {
		IsError bool `json:"isError"`
	}
	if r.Error != nil || json.Unmarshal(r.Result, &result) != nil || !result.IsError {
		t.Fatal("valid tool did not reject mismatched or stale record")
	}
	s.trace = append(s.trace, sixCaseTrace{Tool: tool, Arguments: args, Result: r.Result})
}

func TestIntegrationCoreLabUsageSequencesMCP(t *testing.T) {
	s := newCoreLabSession(t)
	defer s.finish(t)
	a, b, c := s.claim(t, "parent-a"), s.claim(t, "parent-b"), s.claim(t, "parent-c")
	ab, qc := s.claim(t, "from-a-and-b", a, b), s.claim(t, "from-c", c)
	key := evidenceingestion.PropositionKey{Namespace: "synthetic", LocalID: "release-allowed", ScopeRef: "prod", Revision: "1"}
	bind := func(node, id string) map[string]any {
		return map[string]any{"request_id": id, "node_id": node, "key": key, "definition": "Externally assigned release identity", "decision_reason": "TEST APPROVAL STUB"}
	}
	first := bind(ab, "bind-ab")
	sixCaseCall[evidenceingestion.PropositionBindingReceipt](t, s.recorder, &s.trace, "bind_canonical_proposition", first)
	sixCaseCall[evidenceingestion.PropositionBindingReceipt](t, s.recorder, &s.trace, "bind_canonical_proposition", bind(qc, "bind-c"))
	members := func(limit int) evidenceingestion.PropositionMembers {
		return sixCaseCall[evidenceingestion.PropositionMembers](t, s.query, &s.trace, "get_proposition_members", map[string]any{"key": key, "limit": limit})
	}
	t.Run("members_are_not_graph_neighbors_and_AND_parents_stay_separate", func(t *testing.T) {
		limited, all := members(1), members(32)
		want := []string{ab, qc}
		slices.Sort(want)
		if !limited.Truncated || len(limited.Nodes) != 1 || all.Truncated || !slices.Equal(all.Nodes, want) || slices.Contains(all.Nodes, a) || len(all.Graph.Nodes) <= len(all.Nodes) {
			t.Fatal("member inventory collapsed into graph or lost truncation")
		}
		var counts []int
		for _, d := range all.Graph.Derivations {
			counts = append(counts, len(d.Parents))
		}
		slices.Sort(counts)
		if !slices.Equal(counts, []int{1, 2}) {
			t.Fatal("alternative derivations were flattened")
		}
	})
	t.Run("withdraw_then_replay_does_not_restore_and_stale_change_is_rejected", func(t *testing.T) {
		change := map[string]any{"request_id": "withdraw-ab", "node_id": ab, "expected_revision": 0, "previous_ref": "membership:bind-ab", "from_id": key.ID(), "operation": "withdraw", "reason": "TEST APPROVAL STUB wrong identity", "evidence_ref": "mock:correction"}
		withdrawn := sixCaseCall[evidenceingestion.PropositionChangeReceipt](t, s.recorder, &s.trace, "change_proposition_binding", change)
		replay := sixCaseCall[evidenceingestion.PropositionBindingReceipt](t, s.recorder, &s.trace, "bind_canonical_proposition", first)
		if !replay.Replayed || replay.PropositionID != key.ID() || withdrawn.Event.Revision != 1 {
			t.Fatal("historical receipt changed")
		}
		h := sixCaseCall[evidenceingestion.PropositionBindingHistory](t, s.query, &s.trace, "get_proposition_binding_history", map[string]any{"node_id": ab, "revision": -1, "limit": 10})
		if h.Active || h.PropositionID != "" || h.HeadRevision != 1 || !slices.Equal(members(32).Nodes, []string{qc}) {
			t.Fatal("old success resurrected withdrawn binding or erased alternative")
		}
		stale := map[string]any{}
		for k, v := range change {
			stale[k] = v
		}
		stale["request_id"] = "competing-withdraw"
		s.rejected(t, "change_proposition_binding", stale)
		old := sixCaseCall[evidenceingestion.PropositionBindingHistory](t, s.query, &s.trace, "get_proposition_binding_history", map[string]any{"node_id": ab, "revision": 0, "limit": 10})
		if !old.Active || old.PropositionID != key.ID() || old.HeadRevision != 1 {
			t.Fatal("historical identity lost after withdrawal")
		}
	})
	// Revision is part of the four-field dependency identity. The JSON contains
	// both reference tokens so its bytes can stay identical while the declared
	// envelope selects a different pointer and dependency revision.
	subject := s.subject(t, "dependency-scenario", "Synthetic attachment-dependent rule", "Synthetic attachment-dependent rule")
	d1 := evidenceingestion.ExternalDependencyKey{Namespace: "attachment", LocalID: "annex-a", ScopeRef: "prod", Revision: "1"}
	d2 := d1
	d2.Revision = "2"
	content := fmt.Sprintf(`{"old":%q,"new":%q}`, d1.ID(), d2.ID())
	repr := func(id string, key evidenceingestion.ExternalDependencyKey, pointer string) evidenceingestion.ExternalRepresentationRecord {
		args := coreLabRepresentation(id, "candidate", content, subject, []evidenceingestion.ExternalDependency{{Key: key, Status: "missing", Pointers: []string{pointer}, Reason: "Attachment not supplied"}})
		receipt := sixCaseCall[evidenceingestion.ExternalRepresentationReceipt](t, s.recorder, &s.trace, "record_external_representation", args)
		got := sixCaseCall[evidenceingestion.ExternalRepresentationRecord](t, s.query, &s.trace, "get_external_representation", map[string]any{"representation_id": receipt.Record.ID})
		if !reflect.DeepEqual(got, receipt.Record) || !reflect.DeepEqual(got.Definition, coreLabExpectedRecord[evidenceingestion.ExternalRepresentationInput](t, args)) {
			t.Fatal("complete dependency envelope changed")
		}
		return got
	}
	r1, r2 := repr("repr-d1", d1, "/old"), repr("repr-d2", d2, "/new")
	material := sixCaseCall[evidenceingestion.ExternalCheckMaterial](t, s.query, &s.trace, "get_external_representation_material", map[string]any{"representation_id": r1.ID, "input_id": "ir"})
	check := func(id, result string) evidenceingestion.ExternalCheckRecord {
		args := coreLabCheck(id, subject, []evidenceingestion.ExternalCheckMaterial{material}, []evidenceingestion.ExternalCheckFinding{{Dimension: "formal", Criterion: "External assertion only", InputIDs: []string{"ir"}, Result: result, Detail: "No source fidelity claim"}})
		return sixCaseCall[evidenceingestion.ExternalCheckReceipt](t, s.recorder, &s.trace, "record_external_check", args).Record
	}
	c1 := check("check-d1-pass", "pass")
	link := evidenceingestion.ExternalRepresentationLink{CheckID: c1.ID, InputID: "ir", RepresentationID: r1.ID}
	sixCaseCall[json.RawMessage](t, s.recorder, &s.trace, "link_external_check_representation", link)
	users := func(key evidenceingestion.ExternalDependencyKey, limit int) evidenceingestion.ExternalDependencyUsers {
		return sixCaseCall[evidenceingestion.ExternalDependencyUsers](t, s.query, &s.trace, "get_external_dependency_users", map[string]any{"key": key, "limit": limit})
	}
	t.Run("same_content_different_dependency_envelope_cannot_reuse_check", func(t *testing.T) {
		wrong := link
		wrong.RepresentationID = r2.ID
		s.rejected(t, "link_external_check_representation", wrong)
		one, two := users(d1, 32), users(d2, 32)
		if len(one.Representations) != 1 || len(one.CheckInputs) != 1 || len(two.Representations) != 1 || len(two.CheckInputs) != 0 || one.CheckInputs[0] != link || !reflect.DeepEqual(one.Representations[0], r1) || !reflect.DeepEqual(two.Representations[0], r2) {
			t.Fatal("rejected link leaked or dependency revisions merged")
		}
	})
	t.Run("reverse_index_bounds_are_independent_and_reports_do_not_get_rewritten", func(t *testing.T) {
		for i, result := range []string{"not_checked", "unsupported"} {
			c := check(fmt.Sprintf("check-d1-%d", i), result)
			sixCaseCall[json.RawMessage](t, s.recorder, &s.trace, "link_external_check_representation", evidenceingestion.ExternalRepresentationLink{CheckID: c.ID, InputID: "ir", RepresentationID: r1.ID})
		}
		limited := users(d1, 1)
		if limited.RepresentationsTruncated || !limited.CheckInputsTruncated || len(limited.Representations) != 1 || len(limited.CheckInputs) != 1 {
			t.Fatal("independent reverse-index truncation flags conflated")
		}
		repr("repr-d1-alternative", d1, "/old")
		limited = users(d1, 1)
		all, two := users(d1, 32), users(d2, 32)
		if !limited.RepresentationsTruncated || !limited.CheckInputsTruncated || all.RepresentationsTruncated || all.CheckInputsTruncated || len(all.Representations) != 2 || len(all.CheckInputs) != 3 || len(two.CheckInputs) != 0 {
			t.Fatal("bounded reverse index lost a branch, check or revision")
		}
		old := sixCaseCall[evidenceingestion.ExternalCheckRecord](t, s.query, &s.trace, "get_external_check", map[string]any{"check_id": c1.ID})
		if !reflect.DeepEqual(old, c1) {
			t.Fatal("new dependencies or results rewrote historical PASS")
		}
		list := sixCaseCall[evidenceingestion.ExternalCheckList](t, s.query, &s.trace, "list_external_checks", map[string]any{"proposal_occurrence_id": subject.ProposalOccurrenceID, "limit": 1})
		if !list.Truncated || len(list.Records) != 1 {
			t.Fatal("bounded list pretended completeness")
		}
	})
	// Query scope is explicit. A root-only view cannot prove that its premises
	// have no relationships, even when no budget was exhausted.
	t.Run("root_only_is_not_complete_knowledge", func(t *testing.T) {
		v := sixCaseCall[evidencequerymcp.OpenCanonicalReadViewResponse](t, s.query, &s.trace, "open_canonical_read_view", map[string]any{"root_node_ids": []string{a}, "relations": []string{"derived_from"}, "max_depth": 0, "max_nodes": 32, "max_edges": 32})
		if v.View.GlobalAbsenceInferenceAllowed || v.View.MaxDepth != 0 || len(v.Artifact.Edges) != 0 {
			t.Fatal("root-only scope claimed global knowledge")
		}
		full := sixCaseCall[evidencequerymcp.OpenCanonicalReadViewResponse](t, s.query, &s.trace, "open_canonical_read_view", map[string]any{"root_node_ids": []string{ab}, "relations": []string{"derived_from"}, "max_depth": 3, "max_nodes": 32, "max_edges": 32})
		parents := map[string]bool{}
		for _, e := range full.Artifact.Edges {
			if e.To == ab && e.Relation == evidencegraph.CanonicalDerivedFrom {
				parents[e.From] = true
			}
		}
		if !parents[a] || !parents[b] {
			t.Fatal("withdrawal of identity destroyed historical parent edges")
		}
	})
	t.Run("partial_derivation_is_rejected_instead_of_omitting_required_parents", func(t *testing.T) {
		args := map[string]any{"root_node_ids": []string{ab}, "relations": []string{"derived_from"}, "max_depth": 0, "max_nodes": 32, "max_edges": 32}
		response := s.query.request(t, "tools/call", map[string]any{"name": "open_canonical_read_view", "arguments": args})
		var envelope struct {
			IsError           bool `json:"isError"`
			StructuredContent struct {
				Code string `json:"code"`
			} `json:"structuredContent"`
		}
		if response.Error != nil || json.Unmarshal(response.Result, &envelope) != nil || !envelope.IsError || envelope.StructuredContent.Code == "" {
			t.Fatal("partial derivation did not fail closed with a structured error")
		}
		s.trace = append(s.trace, sixCaseTrace{Tool: "open_canonical_read_view", Arguments: args, Result: response.Result})
	})
	t.Run("premise_conflict_is_preserved_without_inventing_a_conclusion_conflict", func(t *testing.T) {
		counter := s.claim(t, "counter-to-parent-a")
		// The standard recorder is not a relation reviewer. Seed only through the
		// native admission API, then observe through the standard read-only MCP.
		proposal, err := evidenceingestion.SubmitCanonicalContradictionProposal(s.ctx, s.fixture.pool, evidenceingestion.CanonicalContradictionProposalInput{
			RequestID: "premise-conflict", NodeAID: a, NodeBID: counter, Rationale: "TEST APPROVAL STUB synthetic premise conflict", ProducerName: "synthetic fixture", ProducerVersion: "1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := evidenceingestion.AdmitPendingCanonicalContradiction(s.ctx, s.fixture.pool, evidenceingestion.CanonicalContradictionAdmissionInput{
			ProposalID: proposal.Proposal.ID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "Preserve relation only; no truth selection",
		}); err != nil {
			t.Fatal(err)
		}
		v := sixCaseCall[evidencequerymcp.OpenCanonicalReadViewResponse](t, s.query, &s.trace, "open_canonical_read_view", map[string]any{
			"root_node_ids": []string{ab, qc}, "relations": []string{"derived_from", "contradicts"}, "max_depth": 3, "max_nodes": 32, "max_edges": 32,
		})
		var conflicts int
		for _, edge := range v.Artifact.Edges {
			if edge.Relation == evidencegraph.CanonicalContradicts {
				conflicts++
				if !((edge.From == a && edge.To == counter) || (edge.To == a && edge.From == counter)) {
					t.Fatal("premise conflict promoted to an inferred conclusion conflict")
				}
			}
		}
		if conflicts != 1 || len(v.Artifact.Derivations) != 2 || v.View.Truncated {
			t.Fatal("premise conflict or alternative derivation omitted")
		}
	})
}
