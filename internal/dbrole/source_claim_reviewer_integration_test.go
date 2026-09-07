//go:build integration

package dbrole

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The reviewer role permits the trusted exact-review API's DML. It is not a
// row-level human-approval policy or a universal cutover of legacy typed APIs.
func TestIntegrationSourceClaimReviewerRoleExactReviewAndBoundaries(t *testing.T) {
	ctx, pools := sourceClaimRoleTestPools(t)
	reviewer, intake, query := pools[ProfileSourceClaimReviewer], pools[ProfileIntake], pools[ProfileQuery]
	manifest, err := BuildManifest(ProfileSourceClaimReviewer)
	if err != nil || !manifest.TrustedRawDML {
		t.Fatal("reviewer manifest must retain the trusted-native-writer boundary")
	}

	for _, sourceKind := range []string{"manual", "external"} {
		t.Run(sourceKind, func(t *testing.T) {
			pending := sourceClaimRolePending(t, ctx, intake, sourceKind)
			snapshot, err := evidenceingestion.LoadReviewableSourceClaimReviewSnapshot(ctx, reviewer, pending.ExtractionAttemptID, pending.ProposalOccurrenceID)
			if err != nil {
				t.Fatalf("reviewer snapshot failed: %v", err)
			}
			readOnlySnapshot, err := evidenceingestion.LoadReviewableSourceClaimReviewSnapshot(ctx, query, pending.ExtractionAttemptID, pending.ProposalOccurrenceID)
			if err != nil || !reflect.DeepEqual(snapshot, readOnlySnapshot) {
				t.Fatal("query role did not read the same immutable review snapshot")
			}
			display, subject, err := evidenceingestion.BuildSourceClaimReviewDisplayArtifact(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			basis := snapshot.ReviewPackage.ProposalBasis
			if sourceKind == "manual" && (basis.SourceTitle != "" || basis.SourceLocation != "" || basis.SourceCoverage != "" || basis.SourceLimitations == nil || len(basis.SourceLimitations) != 0) {
				t.Fatal("plain manual source acquired unimplemented qualification")
			}
			if sourceKind == "external" && (basis.SourceTitle != "Synthetic role fixture" || basis.SourceCoverage != evidenceingestion.ExternalSourceCoverageFullDocument) {
				t.Fatal("synthetic external source lost its exact envelope metadata")
			}
			input := evidenceingestion.ReviewedSourceClaimAdmissionInput{
				ExtractionAttemptID: pending.ExtractionAttemptID, ExpectedSubject: subject,
				DecisionBy: "test-stub:role-reviewer", DecisionReason: "APPROVAL STUB: synthetic role acceptance only; not human approval",
			}
			before := sourceClaimRoleCounts(t, ctx, query)
			for _, profile := range []Profile{ProfileQuery, ProfileIntake} {
				_, err := evidenceingestion.AdmitReviewedSourceClaim(ctx, pools[profile], input)
				sourceClaimRolePermissionDenied(t, err)
				if got := sourceClaimRoleCounts(t, ctx, query); !reflect.DeepEqual(got, before) {
					t.Fatalf("%s attempted admission changed persisted state", profile)
				}
			}
			forged := input
			// Use a syntactically valid different digest so this tests comparison,
			// not only the transport/native ID format validator.
			forged.ExpectedSubject.ReviewDisplayArtifactID = input.ExpectedSubject.ReviewDisplayArtifactID[:len(input.ExpectedSubject.ReviewDisplayArtifactID)-1] + "0"
			if forged.ExpectedSubject.ReviewDisplayArtifactID == input.ExpectedSubject.ReviewDisplayArtifactID {
				forged.ExpectedSubject.ReviewDisplayArtifactID = input.ExpectedSubject.ReviewDisplayArtifactID[:len(input.ExpectedSubject.ReviewDisplayArtifactID)-1] + "1"
			}
			_, err = evidenceingestion.AdmitReviewedSourceClaim(ctx, reviewer, forged)
			sourceClaimRoleDomainError(t, err, evidenceingestion.ErrorReviewContractConflict)
			if got := sourceClaimRoleCounts(t, ctx, query); !reflect.DeepEqual(got, before) {
				t.Fatal("changed display subject wrote state")
			}
			admitted, err := evidenceingestion.AdmitReviewedSourceClaim(ctx, reviewer, input)
			if err != nil || admitted.Replayed || admitted.AdmissionOutcome != "admitted" {
				t.Fatalf("reviewer exact admission failed: %v", err)
			}
			var payload, artifactID, principal, contract string
			if err := query.QueryRow(ctx, `SELECT b.review_display_payload_utf8,b.review_display_artifact_id,d.decision_by,d.review_binding_contract_version
				FROM canonical_source_claim_review_bindings b JOIN admission_decisions d USING(admission_decision_id)
				WHERE b.admission_decision_id=$1`, admitted.AdmissionDecisionID).Scan(&payload, &artifactID, &principal, &contract); err != nil {
				t.Fatalf("read exact review binding through query role: %v", err)
			}
			if payload != display.PayloadUTF8 || artifactID != display.ID || principal != input.DecisionBy || contract != evidenceingestion.ReviewedSourceClaimAdmissionV1 {
				t.Fatal("committed display, reviewer or decision mode differs")
			}
			canonical, err := evidenceingestion.GetCanonicalEvidenceByID(ctx, query, admitted.CanonicalRef)
			if err != nil || canonical.Payload.Claim != basis.StatementText || canonical.OriginProposalOccurrenceID != pending.ProposalOccurrenceID {
				t.Fatalf("query canonical readback differs: %v", err)
			}
			if canonical.OriginProposal.AdmissionOutcome != "admitted" || len(canonical.OriginProposal.SourceRefs) != 1 || canonical.OriginProposal.SourceRefs[0].QuotedText != "Source collection belongs to Detective." {
				t.Fatal("query origin did not retain the exact grounded source")
			}
			after := sourceClaimRoleCounts(t, ctx, query)
			replay, err := evidenceingestion.AdmitReviewedSourceClaim(ctx, reviewer, input)
			if err != nil || !replay.Replayed || replay.AdmissionDecisionID != admitted.AdmissionDecisionID {
				t.Fatalf("restricted reviewer exact replay failed: %v", err)
			}
			for _, changedField := range []string{"reviewer", "reason"} {
				changed := input
				if changedField == "reviewer" {
					changed.DecisionBy = "test-stub:another-reviewer"
				} else {
					changed.DecisionReason += " changed"
				}
				_, err := evidenceingestion.AdmitReviewedSourceClaim(ctx, reviewer, changed)
				sourceClaimRoleDomainError(t, err, evidenceingestion.ErrorAdmissionReplayConflict)
			}
			if got := sourceClaimRoleCounts(t, ctx, query); !reflect.DeepEqual(got, after) {
				t.Fatal("exact or conflicting reviewer replay changed persisted state")
			}
		})
	}

	t.Run("reviewer_write_and_function_boundaries", func(t *testing.T) {
		before := sourceClaimRoleCounts(t, ctx, query)
		_, err := evidenceingestion.CaptureManualSource(ctx, reviewer, evidenceingestion.ManualTextInput{
			SourceID: "reviewer-forbidden-source", SourceVersion: "v1", RequestID: "reviewer-forbidden-source", Raw: []byte("A synthetic source.\n"),
		})
		sourceClaimRolePermissionDenied(t, err)
		for _, statement := range []string{
			`INSERT INTO source_snapshots DEFAULT VALUES`,
			`INSERT INTO proposal_occurrences DEFAULT VALUES`,
			`INSERT INTO canonical_derivations DEFAULT VALUES`,
			`INSERT INTO canonical_derivation_parents DEFAULT VALUES`,
			`INSERT INTO canonical_contradiction_proposals DEFAULT VALUES`,
			`INSERT INTO canonical_contradiction_admission_decisions DEFAULT VALUES`,
			`INSERT INTO canonical_supersession_admission_events DEFAULT VALUES`,
			`INSERT INTO repository_generation_activation_requests DEFAULT VALUES`,
			`UPDATE canonical_graph_nodes SET canonical_node_id=canonical_node_id`,
			`DELETE FROM canonical_graph_edges`,
			`UPDATE admission_decisions SET decision_reason='changed'`,
			`DELETE FROM canonical_source_claim_review_bindings`,
			`TRUNCATE canonical_ordinary_admission_manifests`,
			`SELECT canonical_source_claim_review_binding_assert_v1('synthetic-unavailable')`,
			`SELECT canonical_supersession_stable_id('denied', ARRAY['input'])`,
			`CREATE TEMP TABLE reviewer_forbidden_temp(id TEXT)`,
			`SET ROLE pg_write_all_data`,
		} {
			_, err := reviewer.Exec(ctx, statement)
			sourceClaimRolePermissionDenied(t, err)
		}
		var executable int
		if err := reviewer.QueryRow(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
			WHERE n.nspname=current_schema() AND has_function_privilege(current_user,p.oid,'EXECUTE')`).Scan(&executable); err != nil || executable != 0 {
			t.Fatal("reviewer acquired directly executable native helpers")
		}
		if got := sourceClaimRoleCounts(t, ctx, query); !reflect.DeepEqual(got, before) {
			t.Fatal("denied reviewer operation changed persisted state")
		}
	})
}

func sourceClaimRolePending(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceKind string) evidenceingestion.IngestResult {
	t.Helper()
	const raw = "Source collection belongs to Detective.\n"
	var source evidenceingestion.SourceIntakeResult
	if sourceKind == "manual" {
		captured, err := evidenceingestion.CaptureManualSource(ctx, pool, evidenceingestion.ManualTextInput{
			SourceID: "role-manual", SourceVersion: "v1", RequestID: "role-manual", Raw: []byte(raw),
			OriginMetadata: map[string]string{"title": "arbitrary unqualified metadata"},
		})
		if err != nil {
			t.Fatal(err)
		}
		source = captured
	} else {
		captured, err := evidenceingestion.CaptureExternalSource(ctx, pool, evidenceingestion.ExternalSourceEnvelopeV1{
			SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "role-external",
			SourceSystem: "fixture", SourceNamespace: "review-role", ObjectType: "document", ObjectID: "synthetic", Revision: "v1",
			Title: "Synthetic role fixture", SourceLocation: "https://example.invalid/role-fixture",
			ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
			Content: raw, Coverage: evidenceingestion.ExternalSourceCoverageFullDocument, Limitations: []string{},
			CollectorID: "test-stub:Detective", ConnectorID: "synthetic-only", ObservedAt: "2026-09-07T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		source = captured.SourceIntakeResult
	}
	input, err := evidenceingestion.BuildExtractorInput(ctx, pool, source.ExtractionViewID)
	if err != nil || len(input.Spans) != 1 || input.RenderedText != raw {
		t.Fatal("intake did not preserve synthetic source bytes")
	}
	result, err := evidenceingestion.SubmitExtractorOutput(ctx, pool, evidenceingestion.ExtractorOutputInput{
		RequestID: "role-extract-" + sourceKind, SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
		ProducerSessionRef: "test-session:first", ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "role-fixture", Version: "v1"},
		Output: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{
			ProposalLocalID: "ownership", StatementText: "Source collection belongs to Detective.", EvidenceRefs: []string{input.Spans[0].SpanID},
		}}},
	})
	if err != nil || result.ProposalCount != 1 {
		t.Fatalf("intake did not create one pending proposal: %v", err)
	}
	return result
}

func sourceClaimRolePermissionDenied(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("operation must fail with SQLSTATE 42501, got %v", err)
	}
}

func sourceClaimRoleDomainError(t *testing.T, err error, want evidenceingestion.ErrorKind) {
	t.Helper()
	var domainErr *evidenceingestion.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != want {
		t.Fatalf("error=%v, want domain kind %s", err, want)
	}
}

func sourceClaimRoleCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, len(queryTables))
	for _, table := range queryTables {
		var count int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
			t.Fatalf("count fixture table %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func sourceClaimRoleTestPools(t *testing.T) (context.Context, map[Profile]*pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("cannot connect to the explicitly selected role acceptance database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal("cannot inspect acceptance database")
	}
	public, err := loadPublicDatabaseAuthority(ctx, admin, database)
	if err != nil || public.create || public.temporary {
		t.Fatal("acceptance database must already deny PUBLIC CREATE/TEMPORARY")
	}
	suffix := policyRandomHex(t, 8)
	schema := "ahe_dbrole_review_" + suffix
	schemaID := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaID); err != nil {
		t.Fatal("cannot create private reviewer fixture schema")
	}
	var createdRoles []string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaID+" CASCADE"); err != nil {
			t.Error("cannot remove private reviewer fixture schema")
		}
		for i := len(createdRoles) - 1; i >= 0; i-- {
			roleID := pgx.Identifier{createdRoles[i]}.Sanitize()
			if _, err := admin.Exec(cleanupCtx, "DROP OWNED BY "+roleID); err != nil {
				t.Error("cannot remove private reviewer fixture role grants")
			}
			if _, err := admin.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+roleID); err != nil {
				t.Error("cannot remove private reviewer fixture role")
			}
		}
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid role acceptance database configuration")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	migrationPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot construct private migrator pool")
	}
	t.Cleanup(migrationPool.Close)
	if _, err := migrations.ApplyUpInSchema(ctx, migrationPool, schema); err != nil {
		t.Fatalf("apply native reviewer migrations: %v", err)
	}
	pools := make(map[Profile]*pgxpool.Pool)
	for i, profile := range []Profile{ProfileQuery, ProfileIntake, ProfileSourceClaimReviewer} {
		roleSuffix := string(rune('a'+i)) + "_" + suffix
		group, login := "ahe_review_group_"+roleSuffix, "ahe_review_login_"+roleSuffix
		groupID, loginID := pgx.Identifier{group}.Sanitize(), pgx.Identifier{login}.Sanitize()
		password := policyRandomHex(t, 24)
		for _, operation := range []struct{ statement, role string }{
			{"CREATE ROLE " + groupID + " NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS", group},
			{"CREATE ROLE " + loginID + " LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + password + "'", login},
			{"GRANT " + groupID + " TO " + loginID + " WITH ADMIN FALSE, INHERIT FALSE, SET TRUE", ""},
			{"GRANT CONNECT ON DATABASE " + pgx.Identifier{database}.Sanitize() + " TO " + loginID, ""},
		} {
			if _, err := admin.Exec(ctx, operation.statement); err != nil {
				t.Fatal("cannot provision private reviewer fixture role")
			}
			if operation.role != "" {
				createdRoles = append(createdRoles, operation.role)
			}
		}
		if _, err := InstallPolicy(ctx, admin, InstallInput{Role: group, Schema: schema, Profile: profile}); err != nil {
			t.Fatalf("install %s profile: %v", profile, err)
		}
		runtimeConfig := config.Copy()
		runtimeConfig.ConnConfig.User, runtimeConfig.ConnConfig.Password = login, password
		delete(runtimeConfig.ConnConfig.RuntimeParams, "search_path")
		delete(runtimeConfig.ConnConfig.RuntimeParams, "role")
		pool, binding, err := OpenRuntimePool(ctx, runtimeConfig, RuntimePoolInput{Role: group, Schema: schema, Profile: profile})
		if err != nil {
			t.Fatalf("open restricted %s pool: %v", profile, err)
		}
		t.Cleanup(pool.Close)
		if err := ValidateRuntimeBinding(binding, profile); err != nil {
			t.Fatalf("validate %s binding: %v", profile, err)
		}
		if _, err := migrations.VerifyCurrentInSchema(ctx, pool, schema); err != nil {
			t.Fatalf("verify current native schema through %s: %v", profile, err)
		}
		pools[profile] = pool
	}
	return ctx, pools
}
