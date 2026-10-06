//go:build integration

package migrations

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func rollbackCandidateRelationSourceForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	down, err := os.ReadFile("000055_evidence_ingestion_candidate_relation_source.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, candidateRelationSourceMigration); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationCandidateRelationUpgrade54PreservesHistory(t *testing.T) {
	ctx, pool, schema := migrationTestPoolWithMaxConns(t, 0)
	applyMigrationsThrough(t, ctx, pool, "000054_evidence_consistency_contract.up.sql")
	claim := func(id, text string) string {
		t.Helper()
		p, e := evidenceingestion.IngestManualText(ctx, pool, evidenceingestion.ManualTextInput{SourceID: id, SourceVersion: "v1", Raw: []byte(text + "\n"), RequestID: id, AttemptNumber: 1}, evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: text, EvidenceRefs: []string{"span:S1"}}}})
		if e != nil {
			t.Fatal(e)
		}
		a, e := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic fixture source"})
		if e != nil {
			t.Fatal(e)
		}
		return a.CanonicalRef
	}
	a := claim("research-relation-a", "The refund window is seven days.")
	b := claim("research-relation-b", "The refund window is not seven days.")
	// Frozen schema-54 relation from the pre-fix run; a new writer cannot be used
	// against that schema because its source column intentionally does not exist.
	if _, err := pool.Exec(ctx, `INSERT INTO canonical_contradiction_proposals
 (canonical_contradiction_proposal_id,request_id,request_payload_hash,proposal_fingerprint,node_a_id,node_b_id,relation,rationale,producer_name,producer_version,admission_outcome)
 VALUES ('contradiction-proposal:6edcfd35743707fe4603f3a60a574d028c80165962c0b114e8c92aaa94445eaa','research-REPEATABLE READ','sha256:c3e99a98554589907adfe78a49d13496212fb3ab9dd228aa5a7a09174e8d8283','contradiction-fp:2151f07380d13f9d163a5c3451bab17fed0c3bbbd2f6b747cdcd842cec1c3b6c','canon-node:517109e131949c99','canon-node:dd66704fb8a81adb','contradicts','The supplied statements disagree.','frozen-lab-fixture','v1','pending')`); err != nil {
		t.Fatal(err)
	}
	before := ordinaryFenceRows(t, ctx, pool)
	changed, err := ApplyUpInSchema(ctx, pool, schema)
	if err != nil || !changed {
		t.Fatalf("upgrade: %v %v", changed, err)
	}
	after := ordinaryFenceRows(t, ctx, pool)
	// Compare the original relation columns; new nullable provenance is not an old data change.
	var relationRows string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(r)-'source_proposal_occurrence_id' ORDER BY to_jsonb(r)::text)::text FROM canonical_contradiction_proposals r`).Scan(&relationRows); err != nil {
		t.Fatal(err)
	}
	after["canonical_contradiction_proposals"] = relationRows
	for table, rows := range before {
		if table != "schema_migrations" && !reflect.DeepEqual(rows, after[table]) {
			t.Fatalf("upgrade changed old rows in %s", table)
		}
	}
	status, err := VerifyCurrentInSchema(ctx, pool, schema)
	if err != nil || status.AppliedMigrations != 55 {
		t.Fatalf("upgraded readiness: %+v %v", status, err)
	}
	replay, err := evidenceingestion.SubmitCanonicalContradictionProposal(ctx, pool, evidenceingestion.CanonicalContradictionProposalInput{RequestID: "research-REPEATABLE READ", NodeAID: a, NodeBID: b, Rationale: "The supplied statements disagree.", ProducerName: "frozen-lab-fixture", ProducerVersion: "v1"})
	if err != nil || !replay.Replayed || replay.Proposal.SourceProposalOccurrenceID != "" {
		t.Fatalf("legacy exact replay: %+v %v", replay, err)
	}
	read, err := evidenceingestion.GetCanonicalContradictionProposal(ctx, pool, replay.Proposal.ID)
	if err != nil || read.SourceProposal != nil {
		t.Fatalf("legacy relation fabricated a source: %+v %v", read, err)
	}
	changed, err = ApplyUpInSchema(ctx, pool, schema)
	if err != nil || changed {
		t.Fatalf("upgrade not idempotent: %v %v", changed, err)
	}
}

func TestIntegrationVerifyRejectsRelationSourceContractDrift(t *testing.T) {
	for _, query := range []string{
		`ALTER TABLE canonical_contradiction_proposals DISABLE TRIGGER canonical_contradiction_source_guard`,
		`ALTER TABLE canonical_contradiction_proposals DROP CONSTRAINT canonical_contradiction_source_fk`,
		`ALTER TABLE canonical_contradiction_proposals ALTER COLUMN source_proposal_occurrence_id TYPE varchar`,
	} {
		t.Run(query, func(t *testing.T) {
			ctx, pool := migrationTestPool(t)
			if _, err := ApplyUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, query); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrent(ctx, pool); err == nil {
				t.Fatal("source contract drift passed readiness")
			}
		})
	}
}

func TestIntegrationCandidateRelationRollbackPreservesHistory(t *testing.T) {
	for _, kind := range []string{"candidate", "relation-source"} {
		t.Run(kind, func(t *testing.T) {
			ctx, pool := migrationTestPool(t)
			if _, err := ApplyUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			pending := func(id string) string {
				t.Helper()
				got, err := evidenceingestion.IngestManualText(ctx, pool, evidenceingestion.ManualTextInput{SourceID: id, SourceVersion: "v1", Raw: []byte(id + "\n"), RequestID: id, AttemptNumber: 1}, evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: id, EvidenceRefs: []string{"span:S1"}}}})
				if err != nil {
					t.Fatal(err)
				}
				return got.ProposalOccurrenceID
			}
			claim := func(id string) string {
				t.Helper()
				got, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: pending(id), DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic rollback control"})
				if err != nil {
					t.Fatal(err)
				}
				return got.CanonicalRef
			}
			a := claim("parent-a")
			if kind == "candidate" {
				_, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: pending("hypothesis"), DecisionBy: "TEST APPROVAL STUB", DecisionReason: "record hypothesis", Candidate: &evidenceingestion.CandidateAdmissionInput{ParentNodeIDs: []string{a}, Method: "declared", Producer: "fixture", TraceRef: "trace:rollback"}})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := evidenceingestion.SubmitCanonicalContradictionProposal(ctx, pool, evidenceingestion.CanonicalContradictionProposalInput{RequestID: "relation", NodeAID: a, NodeBID: claim("parent-b"), Rationale: "relation-source", ProducerName: "fixture", ProducerVersion: "v1", SourceProposalOccurrenceID: pending("relation-source")})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := ordinaryFenceRows(t, ctx, pool)
			down, err := os.ReadFile("000055_evidence_ingestion_candidate_relation_source.down.sql")
			if err != nil {
				t.Fatal(err)
			}
			_, err = pool.Exec(ctx, string(down))
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "cannot roll back candidate or relation source history" {
				t.Fatalf("want history-preserving rollback rejection: %v", err)
			}
			if after := ordinaryFenceRows(t, ctx, pool); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected rollback changed historical records")
			}
			if _, err := VerifyCurrent(ctx, pool); err != nil {
				t.Fatal(err)
			}
		})
	}
}
