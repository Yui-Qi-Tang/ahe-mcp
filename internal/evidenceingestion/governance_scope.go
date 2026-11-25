package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

type governanceNodeFetcher func(context.Context, []string) (map[string]evidenceuse.Node, error)

type governanceContradictionFetcher func(context.Context, []string) ([]evidenceuse.Contradiction, error)

// collectEvidenceUseScope gathers exactly what evidenceuse.Assess reads for
// roots: the roots and their AND ancestry (the closure), every contradiction
// incident to the closure, and each conflict peer's own node record. A peer's
// ancestry is not followed, because assessment reads only its explicit state.
// IDs absent from the store stay absent, so Assess reports them as missing.
func collectEvidenceUseScope(ctx context.Context, roots []string, fetchNodes governanceNodeFetcher, fetchContradictions governanceContradictionFetcher) (map[string]evidenceuse.Node, []evidenceuse.Contradiction, error) {
	nodes := map[string]evidenceuse.Node{}
	closure := map[string]bool{}
	var frontier []string
	for _, id := range roots {
		if !closure[id] {
			closure[id] = true
			frontier = append(frontier, id)
		}
	}
	for len(frontier) > 0 {
		got, err := fetchNodes(ctx, frontier)
		if err != nil {
			return nil, nil, err
		}
		var next []string
		for _, id := range frontier {
			n, ok := got[id]
			if !ok {
				continue
			}
			nodes[id] = n
			for _, parent := range n.Parents {
				if !closure[parent] {
					closure[parent] = true
					next = append(next, parent)
				}
			}
		}
		if len(nodes) > governanceScopeMaxNodes {
			return nil, nil, governanceScopeNodeLimitError()
		}
		frontier = next
	}
	ids := make([]string, 0, len(closure))
	for id := range closure {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	contradictions, err := fetchContradictions(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	var peers []string
	seen := map[string]bool{}
	for _, e := range contradictions {
		for _, id := range []string{e.Left, e.Right} {
			if !closure[id] && !seen[id] {
				seen[id] = true
				peers = append(peers, id)
			}
		}
	}
	if len(peers) > 0 {
		got, err := fetchNodes(ctx, peers)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range peers {
			if n, ok := got[id]; ok {
				nodes[id] = n
			}
		}
		if len(nodes) > governanceScopeMaxNodes {
			return nil, nil, governanceScopeNodeLimitError()
		}
	}
	return nodes, contradictions, nil
}

func governanceScopeNodeLimitError() error {
	return fmt.Errorf("governance scope node limit exceeded: assessing these IDs needs more than %d nodes", governanceScopeMaxNodes)
}

// loadGovernanceScopeNodes reads the requested canonical nodes with their
// native bindings and derivation parents. parents accumulates parent rows
// across one scoped load.
func loadGovernanceScopeNodes(ctx context.Context, tx sqlTx, ids []string, parents *int) (map[string]evidenceuse.Node, error) {
	nodes := map[string]evidenceuse.Node{}
	rows, err := tx.query(ctx, `SELECT n.canonical_node_id,n.node_kind,n.temporal,p.admission_outcome,coalesce(r.source_snapshot_id,''),coalesce(m.lineage_key,'')
 FROM canonical_graph_nodes n
 LEFT JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
 LEFT JOIN extraction_attempts a USING(extraction_attempt_id)
 LEFT JOIN extraction_runs r USING(extraction_run_id)
 LEFT JOIN canonical_supersession_members m ON m.canonical_node_id=n.canonical_node_id
 WHERE n.canonical_node_id = ANY($1::text[])
 ORDER BY n.canonical_node_id LIMIT $2`, ids, governanceScopeMaxNodes+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, kind, admission, source, lineage string
		var temporal []byte
		if err := rows.Scan(&id, &kind, &temporal, &admission, &source, &lineage); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := nodes[id]; ok {
			rows.Close()
			return nil, fmt.Errorf("ambiguous canonical lineage or source binding")
		}
		n := evidenceuse.Node{Kind: evidencegraph.CanonicalNodeKind(kind), Admission: admission, Withdrawal: evidenceuse.Withdrawal{Status: evidenceuse.WithdrawalNone, Complete: true}, Currentness: evidenceuse.Currentness{LineageKey: lineage, Complete: lineage == ""}, Parents: []string{}, SourceIDs: []string{}, SourcesComplete: source != "", ContradictionsComplete: true}
		n.ParentsComplete = n.Kind != evidencegraph.CanonicalDerivedClaim
		if err := json.Unmarshal(temporal, &n.Temporal); err != nil {
			rows.Close()
			return nil, err
		}
		if source != "" {
			n.SourceIDs = []string{source}
		}
		nodes[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(nodes) > governanceScopeMaxNodes {
		return nil, governanceScopeNodeLimitError()
	}
	rows, err = tx.query(ctx, `SELECT d.node_id,p.parent_node_id,p.canonical_edge_id,e.from_node_id,e.to_node_id,e.relation,d.use_class,coalesce(a.review_binding_contract_version,''),coalesce(h.proposal_occurrence_id,'') FROM canonical_derivations d LEFT JOIN admission_decisions a ON a.proposal_occurrence_id=d.origin_proposal_occurrence_id LEFT JOIN canonical_historical_derivation_reviews h ON h.admission_decision_id=a.admission_decision_id LEFT JOIN canonical_derivation_parents p USING(derivation_id) LEFT JOIN canonical_graph_edges e USING(canonical_edge_id) WHERE d.node_id = ANY($1::text[]) ORDER BY d.node_id,p.parent_node_id LIMIT $2`, ids, governanceScopeMaxParents+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, parent, edge, from, to, relation, useClass, reviewContract, historicalOccurrence string
		if err := rows.Scan(&id, &parent, &edge, &from, &to, &relation, &useClass, &reviewContract, &historicalOccurrence); err != nil {
			rows.Close()
			return nil, err
		}
		*parents++
		n, ok := nodes[id]
		if !ok || from != parent || to != id || relation != string(evidencegraph.CanonicalDerivedFrom) {
			rows.Close()
			return nil, fmt.Errorf("incomplete derivation authority")
		}
		if (useClass != evidencegraph.DerivationOrdinary && useClass != evidencegraph.DerivationHistoricalOnly) ||
			(useClass == evidencegraph.DerivationHistoricalOnly) != (reviewContract == HistoricalDerivationAdmissionV1 && historicalOccurrence != "") ||
			(useClass == evidencegraph.DerivationOrdinary && (historicalOccurrence != "" || reviewContract == HistoricalDerivationAdmissionV1)) {
			rows.Close()
			return nil, fmt.Errorf("derivation use classification authority differs")
		}
		n.UseClass = useClass
		n.Parents = append(n.Parents, parent)
		n.ParentsComplete = true
		nodes[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if *parents > governanceScopeMaxParents {
		return nil, fmt.Errorf("governance parent limit exceeded")
	}
	return nodes, nil
}

// loadGovernanceScopeContradictions reads every contradiction edge with an
// endpoint in ids.
func loadGovernanceScopeContradictions(ctx context.Context, tx sqlTx, ids []string) ([]evidenceuse.Contradiction, error) {
	out := []evidenceuse.Contradiction{}
	rows, err := tx.query(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,provenance FROM canonical_graph_edges WHERE relation='contradicts' AND (from_node_id = ANY($1::text[]) OR to_node_id = ANY($1::text[])) ORDER BY canonical_edge_id LIMIT $2`, ids, governanceScopeMaxContradictions+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e evidenceuse.Contradiction
		var raw []byte
		if err := rows.Scan(&e.ID, &e.Left, &e.Right, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var p evidencegraph.ProvenanceRecord
		if err := json.Unmarshal(raw, &p); err != nil {
			rows.Close()
			return nil, err
		}
		e.ReviewRef = p.ID
		e.Scope = "recorded_explicit_pair"
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > governanceScopeMaxContradictions {
		return nil, fmt.Errorf("governance contradiction limit exceeded")
	}
	return out, nil
}
