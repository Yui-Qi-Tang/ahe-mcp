//go:build integration

package dbrole

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationIntakeRolePersistsOnlySourceAndPendingProposals(t *testing.T) {
	ctx, pool := intakeRuntimeTestPool(t)
	raw, err := os.ReadFile("../evidenceingestion/testdata/manual_refund_policy.txt")
	if err != nil {
		t.Fatal(err)
	}
	fixtureBytes, err := os.ReadFile("../evidenceingestion/testdata/frozen_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var output evidenceingestion.FrozenExtractorOutput
	if err := json.Unmarshal(fixtureBytes, &output); err != nil {
		t.Fatal(err)
	}

	// Exercise the same public Go writers used by the five intake tools,
	// including replay, abstention, failure persistence, and provenance reads.
	manual := evidenceingestion.ManualTextInput{
		SourceID: "dbrole-manual", SourceVersion: "v1", Raw: raw, RequestID: "manual-1",
	}
	manualResult, err := evidenceingestion.IngestManualText(ctx, pool, manual, output)
	if err != nil {
		t.Fatalf("submit manual evidence with bounded role: %v", err)
	}
	manualReplay, err := evidenceingestion.IngestManualText(ctx, pool, manual, output)
	if err != nil || !manualReplay.Replayed || manualReplay.ProposalOccurrenceID != manualResult.ProposalOccurrenceID {
		t.Fatalf("manual replay = %+v, %v", manualReplay, err)
	}
	textSource, err := evidenceingestion.CaptureManualSource(ctx, pool, evidenceingestion.ManualTextInput{
		SourceID: "dbrole-text", SourceVersion: "v1", Raw: raw, RequestID: "text-1",
	})
	if err != nil {
		t.Fatalf("submit text source with bounded role: %v", err)
	}
	external, err := evidenceingestion.CaptureExternalSource(ctx, pool, evidenceingestion.ExternalSourceEnvelopeV1{
		SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1,
		RequestID:     "external-1", SourceSystem: "fixture", SourceNamespace: "dbrole",
		ObjectType: "document", ObjectID: "refund-policy", Revision: "v1",
		SourceLocation: "https://example.invalid/refund-policy", Title: "Public synthetic fixture",
		ContentFormat:   evidenceingestion.ExternalSourceContentFormatMarkdown,
		ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
		Content:         string(raw), Coverage: evidenceingestion.ExternalSourceCoverageFullDocument,
		CollectorID: "dbrole-test", ConnectorID: "synthetic-fixture", ObservedAt: "2026-09-07T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("submit external source with bounded role: %v", err)
	}
	proposalIDs := []string{manualResult.ProposalOccurrenceID}
	for _, source := range []evidenceingestion.SourceIntakeResult{textSource, external.SourceIntakeResult} {
		input, err := evidenceingestion.BuildExtractorInput(ctx, pool, source.ExtractionViewID)
		if err != nil || input.RenderedText != string(raw) || len(input.Spans) == 0 {
			t.Fatalf("get extractor input = %+v, %v", input, err)
		}
		request := evidenceingestion.ExtractorOutputInput{
			RequestID:        "extract-" + source.SourceSnapshotID,
			SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
			ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "dbrole-fixture", Version: "v1"},
			Output:              output,
		}
		result, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, request)
		if err != nil || result.ProposalCount != 1 {
			t.Fatalf("submit extractor output = %+v, %v", result, err)
		}
		proposalIDs = append(proposalIDs, result.ProposalOccurrenceID)
		replay, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, request)
		if err != nil || !replay.Replayed || replay.ProposalOccurrenceID != result.ProposalOccurrenceID {
			t.Fatalf("extractor replay = %+v, %v", replay, err)
		}
		request.RequestID += "-abstain"
		request.Output = evidenceingestion.FrozenExtractorOutput{}
		abstention, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, request)
		if err != nil || abstention.ProposalCount != 0 {
			t.Fatalf("extractor abstention = %+v, %v", abstention, err)
		}
		request.RequestID += "-failure"
		var invalid evidenceingestion.FrozenExtractorOutput
		if err := json.Unmarshal([]byte(`{"proposals":[{"proposal_local_id":"bad","statement_text":"Unsupported claim","evidence_refs":["span:missing"]}]}`), &invalid); err != nil {
			t.Fatal(err)
		}
		request.Output = invalid
		if _, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, request); err == nil {
			t.Fatal("unsupported proposal unexpectedly accepted")
		}
	}
	for _, proposalID := range proposalIDs {
		trace, err := evidenceingestion.TraceProposalProvenance(ctx, pool, proposalID)
		if err != nil || trace.AdmissionOutcome != "pending" {
			t.Fatalf("pending provenance = %+v, %v", trace, err)
		}
	}
	var failedAttempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extraction_attempts WHERE status = 'failed'`).Scan(&failedAttempts); err != nil || failedAttempts != 2 {
		t.Fatalf("persisted failed attempts = %d, error %v, want 2", failedAttempts, err)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_supersession_admission_events"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil || count != 0 {
			t.Fatalf("canonical boundary %s: count %d, error %v", table, count, err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO canonical_graph_nodes DEFAULT VALUES`,
		`INSERT INTO canonical_graph_edges DEFAULT VALUES`,
		`INSERT INTO admission_decisions DEFAULT VALUES`,
		`INSERT INTO canonical_supersession_admission_events DEFAULT VALUES`,
		`INSERT INTO canonical_contradiction_proposals DEFAULT VALUES`,
		`INSERT INTO repository_generation_activation_requests DEFAULT VALUES`,
		`UPDATE proposal_occurrences SET admission_outcome = 'admitted' WHERE FALSE`,
		`UPDATE source_snapshots SET source_version = 'tampered' WHERE FALSE`,
		`DELETE FROM extraction_attempts WHERE FALSE`,
		`TRUNCATE proposal_batches`,
		`CREATE TABLE intake_forbidden_table (id TEXT)`,
		`CREATE TEMP TABLE intake_forbidden_temp (id TEXT)`,
		`SELECT canonical_supersession_stable_id('denied', ARRAY['input'])`,
		`SET ROLE pg_write_all_data`,
	} {
		_, err := pool.Exec(ctx, statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("forbidden intake statement %q: %v, want SQLSTATE 42501", statement, err)
		}
	}
}

func intakeRuntimeTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect dedicated fixture database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	publicAuthority, err := loadPublicDatabaseAuthority(ctx, admin, database)
	if err != nil || publicAuthority.create || publicAuthority.temporary {
		t.Fatalf("dedicated database must already deny PUBLIC CREATE/TEMPORARY: %+v, %v", publicAuthority, err)
	}
	suffix := policyRandomHex(t, 8)
	schema := "ahe_dbrole_intake_" + suffix
	groupRole := "ahe_intake_group_" + suffix
	loginRole := "ahe_intake_login_" + suffix
	schemaID := pgx.Identifier{schema}.Sanitize()
	groupID := pgx.Identifier{groupRole}.Sanitize()
	loginID := pgx.Identifier{loginRole}.Sanitize()
	password := policyRandomHex(t, 24)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaID); err != nil {
		t.Fatal(err)
	}
	var createdRoles []string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		statements := []string{"DROP SCHEMA IF EXISTS " + schemaID + " CASCADE"}
		for i := len(createdRoles) - 1; i >= 0; i-- {
			statements = append(statements, "DROP OWNED BY "+createdRoles[i], "DROP ROLE IF EXISTS "+createdRoles[i])
		}
		for _, statement := range statements {
			if _, err := admin.Exec(cleanupCtx, statement); err != nil {
				t.Errorf("cleanup dedicated intake fixture: %v", err)
			}
		}
	})
	for _, operation := range []struct{ statement, createdRole string }{
		{"CREATE ROLE " + groupID + " NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS", groupID},
		{"CREATE ROLE " + loginID + " LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + password + "'", loginID},
		{"GRANT " + groupID + " TO " + loginID + " WITH ADMIN FALSE, INHERIT FALSE, SET TRUE", ""},
		{"GRANT CONNECT ON DATABASE " + pgx.Identifier{database}.Sanitize() + " TO " + loginID, ""},
	} {
		if _, err := admin.Exec(ctx, operation.statement); err != nil {
			t.Fatalf("provision dedicated intake roles: %v", err)
		}
		if operation.createdRole != "" {
			createdRoles = append(createdRoles, operation.createdRole)
		}
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	migrationPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(migrationPool.Close)
	if _, err := migrations.ApplyUp(ctx, migrationPool); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPolicy(ctx, admin, InstallInput{Role: groupRole, Schema: schema, Profile: ProfileIntake}); err != nil {
		t.Fatal(err)
	}
	runtimeConfig := config.Copy()
	runtimeConfig.ConnConfig.User = loginRole
	runtimeConfig.ConnConfig.Password = password
	delete(runtimeConfig.ConnConfig.RuntimeParams, "search_path")
	delete(runtimeConfig.ConnConfig.RuntimeParams, "role")
	pool, binding, err := OpenRuntimePool(ctx, runtimeConfig, RuntimePoolInput{Role: groupRole, Schema: schema, Profile: ProfileIntake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := ValidateRuntimeBinding(binding, ProfileIntake); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.VerifyCurrent(ctx, pool); err != nil {
		t.Fatalf("verify shipping schema through bounded role: %v", err)
	}
	return ctx, pool
}
