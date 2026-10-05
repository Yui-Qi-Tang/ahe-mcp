//go:build integration

package mcpcorerecords_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpcorerecords"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpquery"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationCoreRecordMCPAuthorityAndHistory(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid fixture configuration")
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	schema := "ahe_core_" + hex.EncodeToString(nonce)
	owner, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal("fixture connection failed")
	}
	defer owner.Close(context.Background())
	if _, err := owner.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	var roles []string
	defer func() {
		c := context.Background()
		if _, err := owner.Exec(c, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
		for i := len(roles) - 1; i >= 0; i-- {
			id := pgx.Identifier{roles[i]}.Sanitize()
			if _, err := owner.Exec(c, "DROP OWNED BY "+id); err != nil {
				t.Error(err)
			}
			if _, err := owner.Exec(c, "DROP ROLE "+id); err != nil {
				t.Error(err)
			}
		}
	}()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	if _, err := owner.Exec(ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	runtime := func(profile dbrole.Profile) (*pgxpool.Pool, dbrole.RuntimeBinding) {
		t.Helper()
		suffix := fmt.Sprint(len(roles))
		group, login := schema+"_g"+suffix, schema+"_u"+suffix
		if _, err := dbrole.ProvisionRuntime(ctx, owner, dbrole.ProvisionInput{Database: cfg.ConnConfig.Database, Schema: schema, Role: group, SessionUser: login, Profile: profile}); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, group, login)
		c := cfg.Copy()
		c.ConnConfig.User = login
		c.ConnConfig.Password = ""
		delete(c.ConnConfig.RuntimeParams, "search_path")
		p, binding, err := dbrole.OpenRuntimePool(ctx, c, dbrole.RuntimePoolInput{Role: group, Schema: schema, Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		return p, binding
	}
	writerPool, _ := runtime(dbrole.ProfileCoreRecords)
	defer writerPool.Close()
	readerPool, binding := runtime(dbrole.ProfileQuery)
	defer readerPool.Close()
	for _, p := range []*pgxpool.Pool{writerPool, readerPool} {
		if _, err := migrations.VerifyCurrentInSchema(ctx, p, schema); err != nil {
			t.Fatalf("restricted startup readiness: %v", err)
		}
	}
	principal := runtimeauth.Principal{ID: "TEST APPROVAL STUB recorder"}
	writer, err := mcpcorerecords.NewBackend(writerPool, principal)
	if err != nil {
		t.Fatal(err)
	}
	queryServer, err := evidencequerymcp.NewServer(readerPool)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := mcpquery.NewQueryRuntimeAuthorization("query-a", binding)
	if err != nil {
		t.Fatal(err)
	}
	query, err := mcpquery.NewAuthorizedQueryBackend(mcpquery.NewBackend(queryServer), auth)
	if err != nil {
		t.Fatal(err)
	}
	call := func(b mcpstdio.Backend, name string, in, out any) {
		t.Helper()
		p, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body, err := b.CallTool(ctx, name, p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != nil {
			if err := json.Unmarshal(body, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	// TEST APPROVAL STUB: synthetic evidence uses native intake/admission, never operational data.
	claim := func(id string, parents ...string) (string, string) {
		t.Helper()
		receipt, err := evidenceingestion.IngestManualText(ctx, pool, evidenceingestion.ManualTextInput{SourceID: id, SourceVersion: "1", Raw: []byte("Synthetic evidence " + id + ".\n"), RequestID: id, AttemptNumber: 1}, evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: "Synthetic evidence " + id + ".", EvidenceRefs: []string{"span:S1"}}}})
		if err != nil {
			t.Fatal(err)
		}
		in := evidenceingestion.AdmissionInput{ProposalOccurrenceID: receipt.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "Synthetic port validation"}
		if len(parents) > 0 {
			in.Derivation = &evidenceingestion.DerivationAdmissionInput{ParentNodeIDs: parents, Method: "declared", Producer: "fixture", TraceRef: "synthetic:" + id}
		}
		a, err := evidenceingestion.AdmitPendingProposal(ctx, pool, in)
		if err != nil {
			t.Fatal(err)
		}
		return a.CanonicalRef, receipt.ProposalOccurrenceID
	}
	parent, _ := claim("parent")
	other, _ := claim("other")
	derived, proposal := claim("derived", parent)
	child, _ := claim("child", derived)
	key := evidenceingestion.PropositionKey{Namespace: "release", LocalID: "safe", ScopeRef: "test", Revision: "1"}
	bind := map[string]any{"request_id": "binding", "node_id": derived, "key": key, "definition": "Externally declared meaning", "decision_reason": "TEST APPROVAL STUB"}
	var first evidenceingestion.PropositionBindingReceipt
	call(writer, "bind_canonical_proposition", bind, &first)
	if first.Initial.DecisionBy != principal.ID {
		t.Fatal("unbound recorder")
	}
	graphBefore, err := evidenceingestion.ReadCanonicalGraphView(ctx, pool, evidenceingestion.CanonicalReadInput{RootNodeIDs: []string{child}, MaxDepth: 8, MaxNodes: 100, MaxEdges: 100})
	if err != nil {
		t.Fatal(err)
	}
	changedKey := key
	changedKey.LocalID = "unrelated"
	change := map[string]any{"request_id": "correct", "node_id": derived, "expected_revision": 0, "previous_ref": "membership:binding", "from_id": key.ID(), "operation": "correct", "target": changedKey, "definition": "Different externally declared meaning", "reason": "TEST APPROVAL STUB correction", "evidence_ref": "synthetic:correction"}
	call(writer, "change_proposition_binding", change, nil)
	graphAfter, err := evidenceingestion.ReadCanonicalGraphView(ctx, pool, evidenceingestion.CanonicalReadInput{RootNodeIDs: []string{child}, MaxDepth: 8, MaxNodes: 100, MaxEdges: 100})
	if err != nil {
		t.Fatal(err)
	}
	// Compare immutable artifact only; PostgreSQL snapshot coordinates may advance.
	if !reflect.DeepEqual(graphBefore.Artifact, graphAfter.Artifact) {
		t.Fatal("binding correction rewrote historical child premises")
	}
	var history evidenceingestion.PropositionBindingHistory
	call(query, "get_proposition_binding_history", map[string]any{"node_id": derived, "revision": 0, "limit": 100}, &history)
	if history.PropositionID != key.ID() || history.HeadRevision != 1 {
		t.Fatal("historical label was replaced by current label")
	}
	call(writer, "change_proposition_binding", map[string]any{"request_id": "withdraw", "node_id": derived, "expected_revision": 1, "previous_ref": "event:correct", "from_id": changedKey.ID(), "operation": "withdraw", "reason": "TEST APPROVAL STUB", "evidence_ref": "synthetic:withdraw"}, nil)
	call(writer, "bind_canonical_proposition", bind, &first)
	if !first.Replayed {
		t.Fatal("retry not historical")
	}
	var members evidenceingestion.PropositionMembers
	call(query, "get_proposition_members", map[string]any{"key": key, "limit": 32}, &members)
	if len(members.Nodes) != 0 {
		t.Fatal("old binding resurrected")
	}
	call(query, "get_proposition_binding_history", map[string]any{"node_id": derived, "revision": -1, "limit": 1}, &history)
	if history.Active || !history.HistoryTruncated {
		t.Fatal("withdrawal/truncation lost")
	}
	var subject evidenceingestion.ExternalCheckSubject
	call(query, "get_external_check_subject", map[string]any{"proposal_occurrence_id": proposal}, &subject)
	dep := evidenceingestion.ExternalDependencyKey{Namespace: "docs", LocalID: "attachment", ScopeRef: "release", Revision: "1"}
	declaration := map[string]any{"contract": evidenceingestion.ExternalRepresentationContract, "request_id": "representation", "subject": subject, "name": "Release condition", "version": "1", "role": "candidate", "format": "opaque-condition-json", "format_version": "1", "producer": "claimed-external-producer", "mapping_claim": "not checked", "content": "{\n \"dep\":\"" + dep.ID() + "\", \"amount\":9007199254740993 }", "dependencies": []evidenceingestion.ExternalDependency{{Key: dep, Status: "missing", Pointers: []string{"/dep"}, Reason: "missing attachment"}}}
	var representation evidenceingestion.ExternalRepresentationReceipt
	call(writer, "record_external_representation", declaration, &representation)
	if representation.Record.Definition.RecordedBy != principal.ID || representation.Record.Definition.Producer != "claimed-external-producer" {
		t.Fatal("recorder and producer conflated")
	}
	var material evidenceingestion.ExternalCheckMaterial
	call(query, "get_external_representation_material", map[string]any{"representation_id": representation.Record.ID, "input_id": "input"}, &material)
	report := map[string]any{"contract": evidenceingestion.ExternalCheckContract, "request_id": "check", "subject": subject, "checker_name": "claimed-checker", "checker_version": "1", "checker_configuration": "synthetic", "run_ref": "test:run", "materials": []evidenceingestion.ExternalCheckMaterial{material}, "findings": []evidenceingestion.ExternalCheckFinding{{Dimension: "source-fidelity", Criterion: "Not evaluated", InputIDs: []string{}, Result: "not_checked", Detail: "No semantic checker"}}, "limitations": []string{"external assertion only"}}
	var check evidenceingestion.ExternalCheckReceipt
	call(writer, "record_external_check", report, &check)
	if check.Record.Report.RecordedBy != principal.ID {
		t.Fatal("checker recorder unbound")
	}
	call(writer, "link_external_check_representation", evidenceingestion.ExternalRepresentationLink{CheckID: check.Record.ID, InputID: "input", RepresentationID: representation.Record.ID}, nil)
	var users evidenceingestion.ExternalDependencyUsers
	call(query, "get_external_dependency_users", map[string]any{"key": dep, "limit": 10}, &users)
	if len(users.Representations) != 1 || len(users.CheckInputs) != 1 {
		t.Fatal("missing reverse links")
	}
	// Different authenticated recorder cannot replay another recorder's request.
	writerB, err := mcpcorerecords.NewBackend(writerPool, runtimeauth.Principal{ID: "other-recorder"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := json.Marshal(bind)
	if _, err := writerB.CallTool(ctx, "bind_canonical_proposition", p); err == nil {
		t.Fatal("cross-principal replay accepted")
	}
	for _, name := range []string{"bind_canonical_proposition", "record_external_check", "admit_pending_proposal"} {
		if _, err := query.CallTool(ctx, name, []byte(`{}`)); err == nil {
			t.Fatal("query write accepted")
		}
	}
	if _, err := readerPool.Exec(ctx, "INSERT INTO external_check_records(check_id,request_id,proposal_occurrence_id,body) SELECT '', '', '', '' WHERE false"); err == nil {
		t.Fatal("query database write accepted")
	}
	if _, err := writerPool.Exec(ctx, "UPDATE canonical_proposition_bindings SET decision_by='forged' WHERE canonical_node_id=$1", derived); err == nil {
		t.Fatal("immutable row changed using lock privilege")
	}
	if _, err := writerPool.Exec(ctx, "INSERT INTO canonical_graph_nodes(canonical_node_id) SELECT $1 WHERE false", other); err == nil {
		t.Fatal("recorder can admit nodes")
	}
	// Reconstructed Query server reads PostgreSQL history after process-local state is discarded.
	restarted, err := evidencequerymcp.NewServer(readerPool)
	if err != nil {
		t.Fatal(err)
	}
	restartQuery := mcpquery.NewBackend(restarted)
	call(restartQuery, "get_proposition_binding_history", map[string]any{"node_id": derived, "revision": -1, "limit": 100}, &history)
	if history.Active || history.HeadRevision != 2 {
		t.Fatal("restart lost withdrawal")
	}
	// Exercise actual stdio serialization, not only direct Go values.
	server, err := mcpstdio.NewServer("query-test", "test", query)
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_external_check","arguments":{"check_id":"` + check.Record.ID + `"}}}` + "\n")
	var output bytes.Buffer
	if err := server.Serve(ctx, input, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte("not_checked")) || bytes.Contains(output.Bytes(), []byte(`"isError":true`)) {
		t.Fatalf("stdio lost external status: %s", output.String())
	}
}
