//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func externalCheckTestSubject(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, version string) ExternalCheckSubject {
	t.Helper()
	statement := "核准且符合附件甲的條件後，才可以發布；附件甲未提供。"
	got, err := IngestManualText(ctx, pool, ManualTextInput{SourceID: name, SourceVersion: version, Raw: []byte(statement), RequestID: name + version, AttemptNumber: 1}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := LoadExternalCheckSubject(ctx, pool, got.ProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	return subject
}

func TestIntegrationExternalCheckRoundTripAndAuthoritySeparation(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "check-source", "1")
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	input := externalCheckFixture(subject, "report-1")
	got, err := RecordExternalCheck(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadExternalCheck(ctx, pool, got.Record.ID)
	if err != nil || !reflect.DeepEqual(read.Report, input) {
		t.Fatalf("read=%+v %v", read, err)
	}
	if read.Report.Findings[0].Result != "pass" || read.Report.Findings[1].Result != "not_checked" || read.Report.Findings[2].Result != "unsupported" {
		t.Fatal("partial result flattened")
	}
	replay, err := RecordExternalCheck(ctx, pool, input)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Record, got.Record) {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	correction := externalCheckFixture(subject, "report-2")
	correction.RevisesID = got.Record.ID
	correction.RevisionReason = "wrong supplied representation"
	correction.Materials[0].Content = `{"condition":false}`
	correction.Findings[0].Result = "fail"
	newer, err := RecordExternalCheck(ctx, pool, correction)
	if err != nil {
		t.Fatal(err)
	}
	again, err := RecordExternalCheck(ctx, pool, input)
	if err != nil || !again.Replayed || !reflect.DeepEqual(again.Record, got.Record) {
		t.Fatal("old replay changed history", err)
	}
	list, err := ReadExternalChecks(ctx, pool, subject.ProposalOccurrenceID, 1)
	if err != nil || !list.Truncated || len(list.Records) != 1 {
		t.Fatalf("bounded=%+v %v", list, err)
	}
	list, err = ReadExternalChecks(ctx, pool, subject.ProposalOccurrenceID, 10)
	if err != nil || list.Truncated || len(list.Records) != 2 {
		t.Fatalf("list=%+v %v", list, err)
	}
	if newer.Record.ID == got.Record.ID || reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
		t.Fatal("report altered canonical/admission authority")
	}
	p, err := GetProposalByOccurrenceID(ctx, pool, subject.ProposalOccurrenceID)
	if err != nil || p.AdmissionOutcome != "pending" {
		t.Fatal("report acted as admission", err)
	}
	// The immutable target survives a separately authorized native admission.
	if _, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: subject.ProposalOccurrenceID}); err != nil {
		t.Fatal(err)
	}
	after, err := LoadExternalCheckSubject(ctx, pool, subject.ProposalOccurrenceID)
	if err != nil || after != subject {
		t.Fatal("lifecycle drift changed subject", err)
	}
	old, err := ReadExternalCheck(ctx, pool, got.Record.ID)
	if err != nil || !reflect.DeepEqual(old, read) {
		t.Fatal("admission rewrote old report", err)
	}
}

func TestIntegrationExternalCheckRejectsDriftWithZeroWrites(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "same-content", "1")
	other := externalCheckTestSubject(t, ctx, pool, "same-content", "2")
	initial, err := RecordExternalCheck(ctx, pool, externalCheckFixture(subject, "original"))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*ExternalCheckInput){
		"source version":      func(v *ExternalCheckInput) { v.Subject.SourceVersion = other.SourceVersion },
		"occurrence identity": func(v *ExternalCheckInput) { v.Subject.ProposalOccurrenceID = other.ProposalOccurrenceID },
		"view identity":       func(v *ExternalCheckInput) { v.Subject.ExtractionViewID = other.ExtractionViewID },
		"fingerprint":         func(v *ExternalCheckInput) { v.Subject.ProposalFingerprint = "wrong" },
		"source hash":         func(v *ExternalCheckInput) { v.Subject.RawContentHash = "wrong" },
		"false verbatim":      func(v *ExternalCheckInput) { v.Materials[0].Format = "verbatim" },
		"cross subject revision": func(v *ExternalCheckInput) {
			v.Subject = other
			v.RevisesID = initial.Record.ID
			v.RevisionReason = "incorrect retarget"
		},
		"same request changed input": func(v *ExternalCheckInput) { v.RequestID = "original"; v.Materials[0].Content = `{"changed":true}` },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := externalCheckFixture(subject, name)
			mutate(&in)
			if _, err := RecordExternalCheck(ctx, pool, in); err == nil {
				t.Fatal("drift accepted")
			}
			assertTableCount(t, ctx, pool, "external_check_records", 1)
		})
	}
	// Equal content with a new request is a distinct recorded operation.
	second := externalCheckFixture(subject, "second-request")
	got, err := RecordExternalCheck(ctx, pool, second)
	if err != nil || got.Replayed || got.Record.ID == initial.Record.ID {
		t.Fatal("request aliases old receipt", err)
	}
	second.CheckerVersion = "2"
	if _, err := RecordExternalCheck(ctx, pool, second); err == nil {
		t.Fatal("second request not bound")
	}
	assertTableCount(t, ctx, pool, "external_check_records", 2)
}

func TestIntegrationExternalCheckConcurrentRequests(t *testing.T) {
	ctx, pool := integrationPool(t)
	s := externalCheckTestSubject(t, ctx, pool, "concurrent", "1")
	input := externalCheckFixture(s, "same-request")
	const n = 8
	start := make(chan struct{})
	results := make(chan ExternalCheckReceipt, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { <-start; r, e := RecordExternalCheck(ctx, pool, input); results <- r; errs <- e })
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	fresh := 0
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for r := range results {
		if !r.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh=%d", fresh)
	}
	assertTableCount(t, ctx, pool, "external_check_records", 1)
	start = make(chan struct{})
	errs = make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			<-start
			in := externalCheckFixture(s, "competing")
			in.CheckerVersion = fmt.Sprint(i)
			_, e := RecordExternalCheck(ctx, pool, in)
			errs <- e
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("competing successes=%d", success)
	}
	assertTableCount(t, ctx, pool, "external_check_records", 2)
}

func externalRecordRolePool(t *testing.T, ctx context.Context, pool *pgxpool.Pool, profile dbrole.Profile) *pgxpool.Pool {
	t.Helper()
	var schema string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := schema + "_" + strings.ReplaceAll(string(profile), "-", "_")
	if len(role) > 63 {
		role = role[:63]
	}
	id := pgx.Identifier{role}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE ROLE "+id+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP OWNED BY "+id)
		_, _ = pool.Exec(context.Background(), "DROP ROLE "+id)
	})
	if _, err := pool.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" TO "+id); err != nil {
		t.Fatal(err)
	}
	manifest, err := dbrole.BuildManifest(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range manifest.Tables {
		privs := []string{}
		for _, p := range rule.Privileges {
			privs = append(privs, string(p))
		}
		if _, err := pool.Exec(ctx, "GRANT "+strings.Join(privs, ",")+" ON TABLE "+pgx.Identifier{schema, rule.Table}.Sanitize()+" TO "+id); err != nil {
			t.Fatal(err)
		}
	}
	cfg := pool.Config()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, e := c.Exec(ctx, "SET ROLE "+id); return e }
	out, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(out.Close)
	return out
}

func TestIntegrationExternalCheckDatabaseGuardsAndRoles(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := externalCheckTestSubject(t, ctx, pool, "guarded", "1")
	writer := externalRecordRolePool(t, ctx, pool, dbrole.ProfileCoreRecords)
	reader := externalRecordRolePool(t, ctx, pool, dbrole.ProfileQuery)
	input := externalCheckFixture(s, "trusted")
	got, err := RecordExternalCheck(ctx, writer, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExternalCheck(ctx, reader, got.Record.ID); err != nil {
		t.Fatal(err)
	}
	input.RequestID = "read-only"
	if _, err := RecordExternalCheck(ctx, reader, input); err == nil {
		t.Fatal("query role wrote report")
	}
	for _, statement := range []string{"UPDATE external_check_records SET body='{}'", "DELETE FROM external_check_records", "TRUNCATE external_check_records"} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatal("owner bypassed append-only guard", statement)
		}
	}
	// Direct SQL cannot disconnect the row identity, body, digest, or input references.
	for _, kind := range []string{"digest", "row_target", "body_request", "unresolved_input", "duplicate_input"} {
		t.Run(kind, func(t *testing.T) {
			in := externalCheckFixture(s, kind)
			target := s.ProposalOccurrenceID
			request := kind
			if kind == "row_target" {
				target = "occ:missing"
			}
			if kind == "body_request" {
				request = "other"
			}
			if kind == "unresolved_input" {
				in.Findings[0].InputIDs = []string{"absent"}
			}
			if kind == "duplicate_input" {
				in.Materials[1].ID = in.Materials[0].ID
			}
			b, e := json.Marshal(in)
			if e != nil {
				t.Fatal(e)
			}
			id := "check:sha256:" + externalDigest(b)
			if kind == "digest" {
				id = "check:sha256:wrong"
			}
			if _, err := writer.Exec(ctx, `INSERT INTO external_check_records(check_id,request_id,proposal_occurrence_id,body) VALUES($1,$2,$3,$4)`, id, request, target, string(b)); err == nil {
				t.Fatal("direct forged record accepted")
			}
			assertTableCount(t, ctx, pool, "external_check_records", 1)
		})
	}

	t.Run("hidden reports fail instead of claiming absence", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `ALTER TABLE external_check_records ENABLE ROW LEVEL SECURITY`); err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, `ALTER TABLE external_check_records DISABLE ROW LEVEL SECURITY`)
		if _, err := ReadExternalChecks(ctx, reader, s.ProposalOccurrenceID, 10); err == nil {
			t.Fatal("hidden reports silently absent")
		}
	})
	down, err := os.ReadFile("../../migrations/000051_evidence_ingestion_external_checks.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("rollback erased history")
	}
	if _, err := ReadExternalCheck(ctx, reader, got.Record.ID); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationExternalCheckVerbatimInputsAndUniqueEnvelope(t *testing.T) {
	ctx, pool := integrationPool(t)
	s := externalCheckTestSubject(t, ctx, pool, "verbatim-inputs", "1")
	in := externalCheckFixture(s, "verbatim-check")
	var sourceText, candidateText string
	if err := pool.QueryRow(ctx, `SELECT convert_from(rendered_content,'UTF8') FROM extraction_views WHERE extraction_view_id=$1`, s.ExtractionViewID).Scan(&sourceText); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT statement_text FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, s.ProposalOccurrenceID).Scan(&candidateText); err != nil {
		t.Fatal(err)
	}
	in.Materials[0].Format = "verbatim"
	in.Materials[0].Content = sourceText
	in.Materials[1].Format = "verbatim"
	in.Materials[1].Content = candidateText
	r, err := RecordExternalCheck(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadExternalCheck(ctx, pool, r.Record.ID)
	if err != nil || !reflect.DeepEqual(got.Report, in) {
		t.Fatal("verbatim inputs not retained", err)
	}
	in.RequestID = "ambiguous-envelope"
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := `{"checker_version":"unbound",` + string(body[1:])
	if _, err := pool.Exec(ctx, `INSERT INTO external_check_records(check_id,request_id,proposal_occurrence_id,body) VALUES($1,$2,$3,$4)`, "check:sha256:"+externalDigest([]byte(ambiguous)), in.RequestID, s.ProposalOccurrenceID, ambiguous); err == nil {
		t.Fatal("ambiguous checker version accepted")
	}
	assertTableCount(t, ctx, pool, "external_check_records", 1)
}
