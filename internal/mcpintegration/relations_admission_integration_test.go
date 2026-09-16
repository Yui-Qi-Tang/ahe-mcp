//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceimplements"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencereferences"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcprelations"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func relationFixture(t *testing.T) (context.Context, authorityProcessFixture) {
	t.Helper()
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	t.Cleanup(cancel)
	return ctx, newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileRelationReviewer)
}

func relationTools(t *testing.T, p *authorityProcess) {
	t.Helper()
	p.assertTools(t, []string{mcprelations.ToolGetImplementsReview, mcprelations.ToolAdmitReviewedImplements, mcprelations.ToolGetReferencesReview, mcprelations.ToolAdmitReviewedReferences})
	for _, name := range []string{"submit_external_source", "submit_extractor_output", "admit_reviewed_source_claim", "admit_pending_proposal", "admit_pending_supersession", "capture_git_repository_snapshot"} {
		p.assertDenied(t, name, map[string]any{})
	}
}

func relationTool[T any](t *testing.T, p *authorityProcess, name string, args any) T {
	t.Helper()
	response := p.request(t, "tools/call", map[string]any{"name": name, "arguments": args})
	var envelope struct {
		IsError bool            `json:"isError"`
		Body    json.RawMessage `json:"structuredContent"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &envelope) != nil {
		t.Fatal("invalid relation tool envelope")
	}
	if envelope.IsError {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(envelope.Body, &failure)
		// Fixtures contain no real source data. Never print any connection string.
		if strings.Contains(failure.Message, "://") || strings.Contains(strings.ToLower(failure.Message), "password") {
			failure.Message = "[details suppressed]"
		}
		t.Fatalf("%s: %s: %s", name, failure.Code, failure.Message)
	}
	var result T
	if err := json.Unmarshal(envelope.Body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func relationRejected(t *testing.T, p *authorityProcess, name string, args any) {
	t.Helper()
	response := p.request(t, "tools/call", map[string]any{"name": name, "arguments": args})
	var envelope struct {
		IsError bool `json:"isError"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &envelope) != nil || !envelope.IsError {
		t.Fatal("unsafe relation request was not rejected")
	}
}

func TestIntegrationRelationsSubprocessReferences(t *testing.T) {
	ctx, f := relationFixture(t)
	source := canonicalReferencesSameSnapshot(t, ctx, f.pool, "wire", true)
	p := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.relations, f.schema, "relation-reviewer")
	relationTools(t, p)
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.intake, f.schema, "intake")
	before := relationCounts(t, ctx, f.pool)
	review := relationTool[evidencereferences.Review](t, p, mcprelations.ToolGetReferencesReview, source.Request)
	if review.Subject.CutID == "" || !strings.Contains(review.Display.PayloadUTF8, source.Request.Reference.Token) {
		t.Fatal("missing exact reference display")
	}
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("review wrote canonical state")
	}
	req := mcprelations.ReferencesAdmissionRequest{RequestID: "wire-reference", Review: review.Request, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "Synthetic explicit relation approval only."}
	query.assertDenied(t, mcprelations.ToolAdmitReviewedReferences, req)
	intake.assertDenied(t, mcprelations.ToolAdmitReviewedReferences, req)
	for _, decision := range []string{"", "admit", "reject", "audit_only"} {
		bad := req
		bad.Decision = decision
		relationRejected(t, p, mcprelations.ToolAdmitReviewedReferences, bad)
	}
	bad := req
	bad.ExpectedSubject.CutID += "changed"
	relationRejected(t, p, mcprelations.ToolAdmitReviewedReferences, bad)
	encoded, _ := json.Marshal(req)
	for _, extra := range []string{`,"reviewer_id":"forged"}`, `,"decision":"approved"}`, `,"receipt":{}}`} {
		forged := json.RawMessage(string(encoded[:len(encoded)-1]) + extra)
		relationRejected(t, p, mcprelations.ToolAdmitReviewedReferences, forged)
	}
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("negative controls wrote state")
	}
	result := relationTool[evidencereferences.AdmissionResult](t, p, mcprelations.ToolAdmitReviewedReferences, req)
	if result.Replayed || result.Edge.From != source.Request.FromNodeID || result.Edge.To != source.Request.ToNodeID || result.Receipt.ReviewerID != "mock:ahe-ingest-mcp" {
		t.Fatal("relation direction or launcher identity lost")
	}
	want := before
	want[1]++
	want[4]++
	if relationCounts(t, ctx, f.pool) != want {
		t.Fatal("relation admission changed nodes or node decisions")
	}
	read := relationTool[evidencequerymcp.RelationProvenanceResponse](t, query, "get_relation_provenance", map[string]any{"canonical_edge_id": result.Edge.ID})
	if read.ReferencesAdmission == nil || read.ReferencesAdmission.ReviewPayloadUTF8 != review.Display.PayloadUTF8 || read.ReferencesAdmission.ReceiptID != result.Receipt.ID {
		t.Fatal("Query did not retain exact independent receipt")
	}
	replay := relationTool[evidencereferences.AdmissionResult](t, p, mcprelations.ToolAdmitReviewedReferences, req)
	if !replay.Replayed || replay.Edge.ID != result.Edge.ID {
		t.Fatal("exact MCP retry did not replay")
	}
	bad = req
	bad.DecisionReason += " changed"
	relationRejected(t, p, mcprelations.ToolAdmitReviewedReferences, bad)

	reverse := canonicalReferencesRequest(t, source.From, source.To, 1, 0, 1, 0, "[[ahe-ref:#alpha]]", "alpha", evidencereferences.ResolverSameSnapshotV1)
	revReview := relationTool[evidencereferences.Review](t, p, mcprelations.ToolGetReferencesReview, reverse)
	reverseReq := mcprelations.ReferencesAdmissionRequest{RequestID: "wire-reverse", Review: reverse, ExpectedSubject: revReview.Subject, Decision: "approved", DecisionReason: req.DecisionReason}
	relationTool[evidencereferences.AdmissionResult](t, p, mcprelations.ToolAdmitReviewedReferences, reverseReq)
	for _, approval := range source.From.Approvals {
		old, err := evidenceingestion.AdmitReviewedSourceClaim(ctx, f.pool, approval)
		if err != nil || !old.Replayed {
			t.Fatalf("independent relation cycle broke original node replay: %v", err)
		}
	}
	// Later overlap must block a new review, not rewrite historical authority.
	canonicalReferencesExtraClaims(t, ctx, f.pool, source.To, "later", 1, 1, true)
	relationRejected(t, p, mcprelations.ToolGetReferencesReview, source.Request)
	if historical := relationTool[evidencereferences.AdmissionResult](t, p, mcprelations.ToolAdmitReviewedReferences, req); !historical.Replayed {
		t.Fatal("historical cut did not replay")
	}
	p.finish(t)
	query.finish(t)
	intake.finish(t)
}

func TestIntegrationRelationsSubprocessImplements(t *testing.T) {
	ctx, f := relationFixture(t)
	input, native := runtimeRecursiveFixture(t, ctx, f.pool, "wire")
	p := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.relations, f.schema, "relation-reviewer")
	relationTools(t, p)
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
	review := relationTool[mcprelations.ImplementsReviewResponse](t, p, mcprelations.ToolGetImplementsReview, input.Review)
	if review.Subject != native.Subject || review.Display != native.Display || len(review.Request.Rules) != 3 {
		t.Fatal("MCP did not show the complete recursive AND subject")
	}
	req := mcprelations.ImplementsAdmissionRequest{RequestID: "wire-implements", Review: review.Request, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "Synthetic explicit reviewed AND mapping."}
	before := relationCounts(t, ctx, f.pool)
	for _, change := range []func(*mcprelations.ImplementsAdmissionRequest){
		func(r *mcprelations.ImplementsAdmissionRequest) { r.Decision = "audit_only" },
		func(r *mcprelations.ImplementsAdmissionRequest) { r.ExpectedSubject.DisplayID += "changed" },
		func(r *mcprelations.ImplementsAdmissionRequest) { r.Review.Rules = nil },
		func(r *mcprelations.ImplementsAdmissionRequest) {
			r.Review.ImplementationNodeID = r.Review.SpecificationNodeID
		},
	} {
		bad := req
		change(&bad)
		relationRejected(t, p, mcprelations.ToolAdmitReviewedImplements, bad)
	}
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("invalid relation wrote state")
	}
	result := relationTool[evidenceimplements.DerivedAdmissionResult](t, p, mcprelations.ToolAdmitReviewedImplements, req)
	if result.Replayed || result.Receipt.ReviewerID != "mock:ahe-ingest-mcp" || result.Edge.From != req.Review.SpecificationNodeID {
		t.Fatal("invalid implements authority")
	}
	want := before
	want[1]++
	want[3]++
	if relationCounts(t, ctx, f.pool) != want {
		t.Fatal("implements created nodes, parent edges or source decisions")
	}
	if replay := relationTool[evidenceimplements.DerivedAdmissionResult](t, p, mcprelations.ToolAdmitReviewedImplements, req); !replay.Replayed {
		t.Fatal("implements exact retry failed")
	}
	read := relationTool[evidencequerymcp.RelationProvenanceResponse](t, query, "get_relation_provenance", map[string]any{"canonical_edge_id": result.Edge.ID})
	if read.ImplementsAdmission == nil || read.ImplementsAdmission.ContractVersion != evidenceimplements.RecursiveAdmissionContract || read.ImplementsAdmission.DisplayPayloadUTF8 != review.Display.PayloadUTF8 {
		t.Fatal("Query lost implements receipt")
	}
	p.finish(t)
	query.finish(t)
}

func relationCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) [5]int {
	t.Helper()
	var c [5]int
	err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_graph_nodes),(SELECT count(*) FROM canonical_graph_edges),(SELECT count(*) FROM admission_decisions),(SELECT count(*) FROM canonical_implements_admissions),(SELECT count(*) FROM canonical_references_admissions)`).Scan(&c[0], &c[1], &c[2], &c[3], &c[4])
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func relationRuntimePool(t *testing.T, ctx context.Context, f authorityProcessFixture) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(f.relations.dsn)
	if err != nil {
		t.Fatal("invalid isolated test connection")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: f.relations.group, Schema: f.schema, Profile: dbrole.ProfileRelationReviewer})
	if err != nil {
		t.Fatal("cannot open restricted relation pool")
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestIntegrationRelationsAtomicityRolesAndParallelReplay(t *testing.T) {
	ctx, f := relationFixture(t)
	source := canonicalReferencesCrossSnapshot(t, ctx, f.pool, "atomic", true)
	pool := relationRuntimePool(t, ctx, f)
	backend, err := mcprelations.NewBackend(pool, runtimeauth.Principal{ID: "test-stub:relation-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	review, err := evidencereferences.LoadReview(ctx, pool, source.Request)
	if err != nil {
		t.Fatal(err)
	}
	req := mcprelations.ReferencesAdmissionRequest{RequestID: "atomic-reference", Review: review.Request, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "Synthetic explicit approval."}
	args, _ := json.Marshal(req)
	before := relationCounts(t, ctx, f.pool)
	for _, statement := range []string{
		"INSERT INTO canonical_graph_nodes DEFAULT VALUES", "INSERT INTO admission_decisions DEFAULT VALUES",
		"INSERT INTO proposal_occurrences DEFAULT VALUES", "UPDATE canonical_graph_edges SET relation=relation WHERE false",
		"DELETE FROM canonical_references_admissions WHERE false", "SELECT canonical_references_insert_assert_v1('forbidden')",
	} {
		_, err := pool.Exec(ctx, statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("role escaped scoped ACL: %v", err)
		}
	}
	// SQL, not just the Go wrapper, rejects a receipt-less edge.
	_, err = pool.Exec(ctx, `INSERT INTO canonical_graph_edges(canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_proposal_occurrence_id) VALUES('canon-edge:synthetic-forged',$1,$2,'references','{}'::jsonb,$3)`, source.Request.FromNodeID, source.Request.ToNodeID, source.From.Proposals[0].ProposalOccurrenceID)
	if err == nil {
		t.Fatal("SQL accepted an unreceipted edge")
	}
	for _, timing := range []string{"BEFORE", "AFTER"} {
		_, err = f.pool.Exec(ctx, `CREATE FUNCTION relation_test_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.relation='references' THEN RAISE EXCEPTION 'synthetic fault'; END IF; RETURN NEW; END $$`)
		if err != nil {
			t.Fatal(err)
		}
		statement := "CREATE TRIGGER relation_test_fault " + timing + " INSERT ON canonical_graph_edges FOR EACH ROW EXECUTE FUNCTION relation_test_fault()"
		if timing == "AFTER" {
			statement = "CREATE CONSTRAINT TRIGGER relation_test_fault AFTER INSERT ON canonical_graph_edges DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION relation_test_fault()"
		}
		if _, err = f.pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
		result, callErr := backend.CallTool(ctx, mcprelations.ToolAdmitReviewedReferences, args)
		if callErr == nil || result != nil || relationCounts(t, ctx, f.pool) != before {
			t.Fatal("fault leaked an edge, receipt or success")
		}
		if _, err = f.pool.Exec(ctx, "DROP TRIGGER relation_test_fault ON canonical_graph_edges; DROP FUNCTION relation_test_fault()"); err != nil {
			t.Fatal(err)
		}
	}
	type outcome struct {
		body json.RawMessage
		err  error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			body, err := backend.CallTool(ctx, mcprelations.ToolAdmitReviewedReferences, args)
			results <- outcome{body, err}
		}()
	}
	fresh, replayed := 0, 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		var a evidencereferences.AdmissionResult
		if err = json.Unmarshal(result.body, &a); err != nil {
			t.Fatal(err)
		}
		if a.Replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != 1 {
		t.Fatal("parallel retry did not converge to one immutable admission")
	}
	want := before
	want[1]++
	want[4]++
	if relationCounts(t, ctx, f.pool) != want {
		t.Fatal("parallel retry duplicated canonical writes")
	}

	next := canonicalReferencesSameSnapshot(t, ctx, f.pool, "stale", true)
	staleReview, err := evidencereferences.LoadReview(ctx, pool, next.Request)
	if err != nil {
		t.Fatal(err)
	}
	canonicalReferencesExtraClaims(t, ctx, f.pool, next.To, "stale-target", 1, 1, true)
	state := relationCounts(t, ctx, f.pool)
	_, err = evidencereferences.AdmitReviewed(ctx, pool, evidencereferences.AdmissionInput{RequestID: "stale", Review: next.Request, ExpectedSubject: staleReview.Subject, Decision: "approved", ReviewerID: "test-stub", DecisionReason: "Synthetic stale decision."})
	if err == nil || relationCounts(t, ctx, f.pool) != state {
		t.Fatal("changed target cut was admitted")
	}
	// Read-only Query cannot acquire this profile's write authority.
	queryConfig, _ := pgxpool.ParseConfig(f.query.dsn)
	queryPool, _, err := dbrole.OpenRuntimePool(ctx, queryConfig, dbrole.RuntimePoolInput{Role: f.query.group, Schema: f.schema, Profile: dbrole.ProfileQuery})
	if err != nil {
		t.Fatal("query role could not open")
	}
	defer queryPool.Close()
	_, err = queryPool.Exec(ctx, "INSERT INTO canonical_references_admissions DEFAULT VALUES")
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatal("query acquired relation admission authority")
	}
	if !reflect.DeepEqual(state, relationCounts(t, ctx, f.pool)) {
		t.Fatal("denied query altered state")
	}
}

// SQL-authority and role checks are exercised against the selected disposable
// database only; no existing database is ever inspected by this fixture.
func TestIntegrationRelationsReadinessRejectsWeakenedAuthority(t *testing.T) {
	ctx, f := relationFixture(t)
	for _, kind := range []string{"implements", "references"} {
		table := "canonical_" + kind + "_admissions"
		trigger := table + "_authority"
		for _, statements := range [][2]string{
			{"ALTER TABLE " + table + " DISABLE TRIGGER " + trigger, "ALTER TABLE " + table + " ENABLE TRIGGER " + trigger},
			{"ALTER TABLE " + table + " ALTER COLUMN decision_reason DROP NOT NULL", "ALTER TABLE " + table + " ALTER COLUMN decision_reason SET NOT NULL"},
			{"ALTER TABLE " + table + " DROP CONSTRAINT " + table + "_pair_uq",
				"ALTER TABLE " + table + " ADD CONSTRAINT " + table + "_pair_uq UNIQUE(" + map[string]string{"implements": "specification_node_id,implementation_node_id", "references": "from_node_id,to_node_id"}[kind] + ")"},
			{"GRANT EXECUTE ON FUNCTION canonical_" + kind + "_admission_trigger_v1() TO PUBLIC", "REVOKE EXECUTE ON FUNCTION canonical_" + kind + "_admission_trigger_v1() FROM PUBLIC"},
		} {
			if _, err := f.pool.Exec(ctx, statements[0]); err != nil {
				t.Fatal(err)
			}
			if _, err := migrations.VerifyCurrentInSchema(ctx, f.pool, f.schema); err == nil {
				t.Fatal("weakened relation authority passed startup readiness")
			}
			if _, err := f.pool.Exec(ctx, statements[1]); err != nil {
				t.Fatal(err)
			}
			if _, err := migrations.VerifyCurrentInSchema(ctx, f.pool, f.schema); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestIntegrationRelationsImplementsAtomicityAndParallelReplay(t *testing.T) {
	ctx, f := relationFixture(t)
	input, review := runtimeRecursiveFixture(t, ctx, f.pool, "atomic-implements")
	pool := relationRuntimePool(t, ctx, f)
	backend, err := mcprelations.NewBackend(pool, runtimeauth.Principal{ID: "test-stub:relation-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	req := mcprelations.ImplementsAdmissionRequest{RequestID: "atomic-implements", Review: input.Review, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "Synthetic AND approval."}
	args, _ := json.Marshal(req)
	before := relationCounts(t, ctx, f.pool)
	for _, timing := range []string{"BEFORE", "AFTER"} {
		_, err = f.pool.Exec(ctx, `CREATE FUNCTION implements_test_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.relation='implements' THEN RAISE EXCEPTION 'synthetic fault'; END IF; RETURN NEW; END $$`)
		if err != nil {
			t.Fatal(err)
		}
		statement := "CREATE TRIGGER implements_test_fault " + timing + " INSERT ON canonical_graph_edges FOR EACH ROW EXECUTE FUNCTION implements_test_fault()"
		if timing == "AFTER" {
			statement = "CREATE CONSTRAINT TRIGGER implements_test_fault AFTER INSERT ON canonical_graph_edges DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION implements_test_fault()"
		}
		if _, err = f.pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
		body, err := backend.CallTool(ctx, mcprelations.ToolAdmitReviewedImplements, args)
		if err == nil || body != nil || relationCounts(t, ctx, f.pool) != before {
			t.Fatal("implements fault leaked canonical state or success")
		}
		if _, err = f.pool.Exec(ctx, "DROP TRIGGER implements_test_fault ON canonical_graph_edges; DROP FUNCTION implements_test_fault()"); err != nil {
			t.Fatal(err)
		}
	}
	type outcome struct {
		body json.RawMessage
		err  error
	}
	out := make(chan outcome, 2)
	for range 2 {
		go func() {
			body, err := backend.CallTool(ctx, mcprelations.ToolAdmitReviewedImplements, args)
			out <- outcome{body, err}
		}()
	}
	fresh, replay := 0, 0
	for range 2 {
		got := <-out
		if got.err != nil {
			t.Fatal(got.err)
		}
		var result evidenceimplements.DerivedAdmissionResult
		if err := json.Unmarshal(got.body, &result); err != nil {
			t.Fatal(err)
		}
		if result.Replayed {
			replay++
		} else {
			fresh++
		}
	}
	want := before
	want[1]++
	want[3]++
	if fresh != 1 || replay != 1 || relationCounts(t, ctx, f.pool) != want {
		t.Fatal("implements parallel replay did not converge")
	}
	req.DecisionReason += "changed"
	args, _ = json.Marshal(req)
	if _, err := backend.CallTool(ctx, mcprelations.ToolAdmitReviewedImplements, args); err == nil {
		t.Fatal("changed review reason replayed")
	}
	if relationCounts(t, ctx, f.pool) != want {
		t.Fatal("changed review mutated state")
	}
}
