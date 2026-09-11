//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationSupersessionReplayRejectsCompleteAdmissionAuditDrift(t *testing.T) {
	tests := []struct {
		name                    string
		relationSpecificClosure bool
		disableTable            string
		disableTrigger          string
		tamper                  func(*testing.T, context.Context, validationAuditExecutor, SupersessionAdmissionResult)
	}{
		{
			name:                    "extra decision edge",
			relationSpecificClosure: true,
			disableTable:            "admission_decisions",
			disableTrigger:          "canonical_supersession_decisions_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				if _, err := pool.Exec(ctx, `
					UPDATE admission_decisions
					SET canonical_edge_ids = canonical_edge_ids || '["canon-edge:unexpected"]'::jsonb
					WHERE admission_decision_id = $1
				`, result.AdmissionDecisionID); err != nil {
					t.Fatalf("append unexpected decision edge: %v", err)
				}
			},
		},
		{
			name:                    "missing supports claim edge",
			relationSpecificClosure: true,
			disableTable:            "canonical_graph_edges",
			disableTrigger:          "canonical_supersession_edges_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				supportEdgeID := validationAuditSupportEdgeID(t, result)
				if _, err := pool.Exec(ctx, `
					DELETE FROM canonical_graph_edges
					WHERE canonical_edge_id = $1
				`, supportEdgeID); err != nil {
					t.Fatalf("delete supports_claim edge: %v", err)
				}
			},
		},
		{
			name:                    "missing raw evidence decision binding",
			relationSpecificClosure: true,
			disableTable:            "admission_decisions",
			disableTrigger:          "canonical_supersession_decisions_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				if _, err := pool.Exec(ctx, `
					UPDATE admission_decisions
					SET raw_evidence_node_ids = '[]'::jsonb
					WHERE admission_decision_id = $1
				`, result.AdmissionDecisionID); err != nil {
					t.Fatalf("clear decision raw evidence IDs: %v", err)
				}
			},
		},
		{
			name:                    "proposal canonical binding drift",
			relationSpecificClosure: true,
			disableTable:            "proposal_occurrences",
			disableTrigger:          "canonical_supersession_proposals_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				if _, err := pool.Exec(ctx, `
					UPDATE proposal_occurrences
					SET canonical_ref = $2
					WHERE proposal_occurrence_id = $1
				`, result.ProposalOccurrenceID, result.TargetNodeIDs[0]); err != nil {
					t.Fatalf("change proposal canonical binding: %v", err)
				}
			},
		},
		{
			name:                    "forged derivation metadata",
			relationSpecificClosure: true,
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				const derivationID = "derivation:audit-forged"
				if _, err := pool.Exec(ctx, `
					INSERT INTO canonical_derivations (
						derivation_id,
						node_id,
						method,
						producer,
						trace_ref,
						provenance_ref,
						origin_proposal_occurrence_id
					)
					VALUES ($1, $2, 'audit-forged', 'audit-test', 'audit-trace', 'audit-provenance', $3)
				`, derivationID, result.CanonicalRef, result.ProposalOccurrenceID); err != nil {
					t.Fatalf("insert forged derivation: %v", err)
				}
				if _, err := pool.Exec(ctx, `
					INSERT INTO canonical_derivation_parents (
						derivation_id,
						parent_node_id,
						canonical_edge_id
					)
					VALUES ($1, $2, $3)
				`, derivationID, result.TargetNodeIDs[0], result.SupersedesEdgeIDs[0]); err != nil {
					t.Fatalf("insert forged derivation parent: %v", err)
				}
			},
		},
		{
			name:                    "replacement node content drift",
			relationSpecificClosure: true,
			disableTable:            "canonical_graph_nodes",
			disableTrigger:          "canonical_supersession_nodes_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				if _, err := pool.Exec(ctx, `
					UPDATE canonical_graph_nodes
					SET payload = jsonb_set(payload, '{audit_tamper}', 'true'::jsonb, true),
						provenance = jsonb_set(
							provenance,
							'{method}',
							to_jsonb('audit-tampered'::text),
							true
						)
					WHERE canonical_node_id = $1
				`, result.CanonicalRef); err != nil {
					t.Fatalf("change replacement node content: %v", err)
				}
			},
		},
		{
			name:                    "supports claim provenance drift",
			relationSpecificClosure: true,
			disableTable:            "canonical_graph_edges",
			disableTrigger:          "canonical_supersession_edges_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				supportEdgeID := validationAuditSupportEdgeID(t, result)
				if _, err := pool.Exec(ctx, `
					UPDATE canonical_graph_edges
					SET provenance = jsonb_set(
						provenance,
						'{method}',
						to_jsonb('audit-tampered'::text),
						true
					)
					WHERE canonical_edge_id = $1
				`, supportEdgeID); err != nil {
					t.Fatalf("change supports_claim provenance: %v", err)
				}
			},
		},
		{
			name:                    "unexpected proposal-attributed edge",
			relationSpecificClosure: true,
			disableTable:            "canonical_graph_edges",
			disableTrigger:          "canonical_supersession_edges_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				supportEdgeID := validationAuditSupportEdgeID(t, result)
				if _, err := pool.Exec(ctx, `
					INSERT INTO canonical_graph_edges (
						canonical_edge_id,
						from_node_id,
						to_node_id,
						relation,
						provenance,
						origin_proposal_occurrence_id
					)
					SELECT
						'canon-edge:audit-unexpected',
						$2,
						$3,
						'references',
						provenance,
						origin_proposal_occurrence_id
					FROM canonical_graph_edges
					WHERE canonical_edge_id = $1
				`, supportEdgeID, result.CanonicalRef, result.RawEvidenceNodeIDs[0]); err != nil {
					t.Fatalf("insert unexpected proposal edge: %v", err)
				}
			},
		},
		{
			name: "unexpected proposal-attributed node",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				if _, err := pool.Exec(ctx, `
					INSERT INTO canonical_graph_nodes (
						canonical_node_id,
						node_kind,
						payload,
						provenance,
						temporal,
						integrity,
						origin_proposal_occurrence_id
					)
					SELECT
						'canon-node:audit-unexpected',
						node_kind,
						payload,
						provenance,
						temporal,
						integrity,
						origin_proposal_occurrence_id
					FROM canonical_graph_nodes
					WHERE canonical_node_id = $1
				`, result.CanonicalRef); err != nil {
					t.Fatalf("insert unexpected proposal node: %v", err)
				}
			},
		},
		{
			name:           "unexpected event member",
			disableTable:   "canonical_supersession_members",
			disableTrigger: "canonical_supersession_members_authority_trigger",
			tamper: func(t *testing.T, ctx context.Context, pool validationAuditExecutor, result SupersessionAdmissionResult) {
				t.Helper()
				if _, err := pool.Exec(ctx, `
					INSERT INTO canonical_supersession_members (
						canonical_node_id,
						lineage_key,
						first_admission_event_id,
						was_bootstrapped
					)
					VALUES ($1, $2, $3, true)
				`, result.RawEvidenceNodeIDs[0], result.LineageKey, result.AdmissionEventID); err != nil {
					t.Fatalf("insert unexpected event member: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, result := validationAuditAdmitPair(t, ctx, pool, test.name)
			guards := validationAuditCorruptionGuards(test.disableTable, test.disableTrigger)
			switch test.name {
			case "forged derivation metadata":
				guards = append(guards,
					validationAuditTrigger{"canonical_derivations", "canonical_ordinary_admission_derivations_authority"},
					validationAuditTrigger{"canonical_derivation_parents", "canonical_ordinary_admission_derivation_parents_authority"},
				)
			case "unexpected proposal-attributed node":
				guards = append(guards, validationAuditTrigger{"canonical_graph_nodes", "canonical_ordinary_admission_nodes_authority"})
			}
			validationAuditCorruptFixture(t, ctx, pool, guards, func(tx validationAuditExecutor) {
				test.tamper(t, ctx, tx, result)
			})
			wantCounts := supersessionAuthorityCounts(t, ctx, pool)
			wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
			if err != nil {
				t.Fatalf("load head after privileged fixture corruption: %v", err)
			}

			if test.relationSpecificClosure {
				// Closure is deliberately relation-specific. Non-supersession audit
				// drift does not certify the full support graph, but it also must not
				// change the exact supersession currentness projection.
				currentness, err := GetCanonicalSupersessionCurrentness(ctx, pool, result.LineageKey)
				if err != nil {
					t.Fatalf("relation-specific currentness after non-supersession drift: %v", err)
				}
				if !currentness.Projection.ClosureAvailable || currentness.Projection.Witness == nil {
					t.Fatalf("relation-specific closure unavailable after non-supersession drift: %+v", currentness)
				}
			}

			_, err = AdmitPendingSupersession(ctx, pool, input)
			assertKind(t, err, ErrorSupersessionReplayConflict)
			validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
		})
	}
}

type validationAuditExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type validationAuditTrigger struct {
	table   string
	trigger string
}

func validationAuditCorruptionGuards(table, trigger string) []validationAuditTrigger {
	if trigger == "" {
		return nil
	}
	guards := []validationAuditTrigger{{table, trigger}}
	switch table {
	case "admission_decisions":
		guards = append(guards,
			validationAuditTrigger{table, "admission_decisions_append_only"},
			validationAuditTrigger{table, "canonical_ordinary_admission_decisions_authority"},
		)
	case "canonical_graph_nodes":
		guards = append(guards,
			validationAuditTrigger{table, "canonical_graph_nodes_append_only"},
			validationAuditTrigger{table, "canonical_ordinary_admission_nodes_authority"},
		)
	case "canonical_graph_edges":
		guards = append(guards,
			validationAuditTrigger{table, "canonical_graph_edges_append_only"},
			validationAuditTrigger{table, "canonical_ordinary_admission_edges_authority"},
		)
	case "proposal_occurrences":
		guards = append(guards,
			validationAuditTrigger{table, "proposal_occurrences_terminal_guard"},
			validationAuditTrigger{table, "canonical_ordinary_admission_proposals_authority"},
		)
	}
	return guards
}

func validationAuditCorruptFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	guards []validationAuditTrigger,
	tamper func(validationAuditExecutor),
) {
	t.Helper()
	// This owner-only malformed-database fixture is not reachable through a
	// valid public writer. Disable named guards only in the disposable schema,
	// restore them in the same transaction, then test the independent reader.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin isolated corruption fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, guard := range guards {
		query := fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER %s", guard.table, guard.trigger)
		if _, err := tx.Exec(ctx, query); err != nil {
			t.Fatalf("disable isolated %s authority trigger: %v", guard.trigger, err)
		}
	}
	tamper(tx)
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("flush remaining fixture constraints: %v", err)
	}
	for _, guard := range guards {
		query := fmt.Sprintf("ALTER TABLE %s ENABLE TRIGGER %s", guard.table, guard.trigger)
		if _, err := tx.Exec(ctx, query); err != nil {
			t.Fatalf("restore isolated %s authority trigger: %v", guard.trigger, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit isolated corruption fixture: %v", err)
	}
}

func validationAuditAdmitPair(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
) (SupersessionAdmissionInput, SupersessionAdmissionResult) {
	t.Helper()
	basis := supersessionIntegrationBasis()
	oldProposal := createSupersessionExternalProposal(t, ctx, pool, "audit-old-"+suffix, "r1", "Policy value is v1.")
	old, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: oldProposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial audit fixture",
	})
	if err != nil {
		t.Fatalf("admit audit old claim: %v", err)
	}
	newProposal := createSupersessionExternalProposal(t, ctx, pool, "audit-new-"+suffix, "r2", "Policy value is v2.")
	input := SupersessionAdmissionInput{
		ProposalOccurrenceID: newProposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "reviewed complete admission audit",
		Basis:                basis,
		TargetNodeIDs:        []string{old.CanonicalRef},
	}
	result, err := AdmitPendingSupersession(ctx, pool, input)
	if err != nil {
		t.Fatalf("admit audit replacement: %v", err)
	}
	return input, result
}

func validationAuditSupportEdgeID(t *testing.T, result SupersessionAdmissionResult) string {
	t.Helper()
	supersedes := make(map[string]struct{}, len(result.SupersedesEdgeIDs))
	for _, edgeID := range result.SupersedesEdgeIDs {
		supersedes[edgeID] = struct{}{}
	}
	for _, edgeID := range result.CanonicalEdgeIDs {
		if _, isSupersedes := supersedes[edgeID]; !isSupersedes {
			return edgeID
		}
	}
	t.Fatal("supersession admission result has no supports_claim edge")
	return ""
}
