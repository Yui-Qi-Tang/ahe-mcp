package evidenceingestion

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/jackc/pgx/v5"
)

// ConsistencyFrontierPolicy retains all classified live frontiers, including
// ambiguous branches. It establishes admitted-snapshot eligibility, not truth.
const ConsistencyFrontierPolicy = "classified-frontier/cnf-v1"

// ConsistencySelection explains participation without altering canonical data.
type ConsistencySelection struct {
	NodeID     string   `json:"node_id"`
	State      string   `json:"state"` // included, excluded, blocked
	Reason     string   `json:"reason"`
	LineageKey string   `json:"lineage_key,omitempty"`
	Parents    []string `json:"parents,omitempty"`
}

// ConsistencyLineage retains the complete currentness projection used by policy.
// It includes dependencies outside the proposition scope, so their changes also
// invalidate a view. The underlying loader validates the entire admission chain.
type ConsistencyLineage struct {
	Projection evidencesupersession.CurrentnessProjection `json:"projection"`
}

func selectConsistencyFrontier(ctx context.Context, tx pgx.Tx, view *ConsistencyView) error {
	ids := make([]string, 0, len(view.Members))
	for _, m := range view.Members {
		ids = append(ids, m.NodeID)
	}
	rows, err := tx.Query(ctx, `SELECT canonical_node_id,lineage_key FROM canonical_supersession_members
		WHERE canonical_node_id=ANY($1::text[]) ORDER BY canonical_node_id COLLATE "C"`, ids)
	if err != nil {
		return err
	}
	nodeLineages := map[string]string{}
	lineages := map[string]bool{}
	for rows.Next() {
		var node, lineage string
		if err := rows.Scan(&node, &lineage); err != nil {
			rows.Close()
			return err
		}
		if _, exists := nodeLineages[node]; exists {
			rows.Close()
			return fmt.Errorf("%w: multiple node lineages", logicresolver.ErrInput)
		}
		nodeLineages[node] = lineage
		lineages[lineage] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	keys := make([]string, 0, len(lineages))
	for key := range lineages {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	statuses := map[string]evidencesupersession.CurrentnessStatus{}
	for _, key := range keys {
		input, err := loadCanonicalSupersessionClosureInput(ctx, pgxTx{tx: tx}, key)
		if err != nil {
			return err
		}
		projection, err := evidencesupersession.ProjectCurrentness(input)
		if err != nil {
			return canonicalSupersessionClosureInvariantError(err)
		}
		view.Currentness = append(view.Currentness, ConsistencyLineage{Projection: projection})
		for _, node := range projection.Nodes {
			statuses[node.NodeID] = node.Status
		}
	}
	view.Selection, err = consistencyEligibility(view.Artifact, nodeLineages, statuses, time.Now().UTC())
	return err
}

func consistencyEligibility(a evidencegraph.CanonicalArtifact, lineages map[string]string, statuses map[string]evidencesupersession.CurrentnessStatus, now time.Time) ([]ConsistencySelection, error) {
	nodes := map[string]evidencegraph.CanonicalNode{}
	temporal := map[string]evidencegraph.TemporalRecord{}
	parents := map[string][]string{}
	for _, n := range a.Nodes {
		nodes[n.ID] = n
	}
	for _, t := range a.Temporal {
		temporal[t.ID] = t
	}
	for _, d := range a.Derivations {
		parents[d.NodeID] = d.Parents
	}
	done := map[string]ConsistencySelection{}
	visiting := map[string]bool{}
	var visit func(string) (ConsistencySelection, error)
	visit = func(id string) (ConsistencySelection, error) {
		if got, ok := done[id]; ok {
			return got, nil
		}
		n, ok := nodes[id]
		if !ok || visiting[id] {
			return ConsistencySelection{}, fmt.Errorf("%w: incomplete or cyclic parents", logicresolver.ErrInput)
		}
		visiting[id] = true
		defer delete(visiting, id)
		s := ConsistencySelection{NodeID: id, State: "blocked", Reason: "unclassified_currentness", LineageKey: lineages[id], Parents: slices.Clone(parents[id])}
		t := temporal[n.TemporalRef]
		window := ""
		var start, end time.Time
		for _, bound := range []struct {
			value, reason string
			start         bool
		}{{t.ValidFrom, "not_yet_valid", true}, {t.ValidTo, "expired", false}} {
			if bound.value == "" {
				continue
			}
			v, err := time.Parse(time.RFC3339Nano, bound.value)
			if err != nil {
				s.Reason = "invalid_validity_window"
				done[id] = s
				return s, nil
			}
			if bound.start {
				start = v
			} else {
				end = v
			}
			if (bound.start && now.Before(v)) || (!bound.start && !now.Before(v)) {
				window = bound.reason
			}
		}
		if !start.IsZero() && !end.IsZero() && !start.Before(end) {
			s.Reason = "invalid_validity_window"
			done[id] = s
			return s, nil
		}
		switch {
		case window != "":
			s.State, s.Reason = "excluded", window
		case statuses[id] == evidencesupersession.CurrentnessSuperseded:
			s.State, s.Reason = "excluded", "superseded"
		case t.Status == evidencegraph.TemporalStale || t.Status == evidencegraph.TemporalSuperseded:
			s.State, s.Reason = "excluded", "recorded_"+string(t.Status)
		case n.Kind == evidencegraph.CanonicalDerivedClaim:
			if len(parents[id]) == 0 {
				return s, fmt.Errorf("%w: missing derivation", logicresolver.ErrInput)
			}
			s.State, s.Reason = "included", "all_parents_eligible"
			for _, parent := range parents[id] {
				p, err := visit(parent)
				if err != nil {
					return s, err
				}
				if p.State == "excluded" {
					s.State, s.Reason = "excluded", "ineligible_parent"
				}
				if p.State == "blocked" && s.State != "excluded" {
					s.State, s.Reason = "blocked", "unknown_parent"
				}
			}
		case statuses[id] == evidencesupersession.CurrentnessCurrent:
			s.State, s.Reason = "included", "classified_current"
		case statuses[id] == evidencesupersession.CurrentnessAmbiguous:
			s.State, s.Reason = "included", "ambiguous_frontier"
		case lineages[id] == "" && t.Status == evidencegraph.TemporalCurrent:
			s.State, s.Reason = "included", "recorded_current"
		}
		done[id] = s
		return s, nil
	}
	var result []ConsistencySelection
	for _, node := range a.Nodes {
		s, err := visit(node.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	slices.SortFunc(result, func(a, b ConsistencySelection) int {
		if a.NodeID < b.NodeID {
			return -1
		}
		if a.NodeID > b.NodeID {
			return 1
		}
		return 0
	})
	return result, nil
}

// consistencySelectedView preserves the full source view in results while
// restricting formula coverage to policy-selected declarations only.
func consistencySelectedView(view ConsistencyView) (ConsistencyView, bool) {
	if view.Profile == ConsistencyProfile {
		return view, true
	}
	included := map[string]bool{}
	complete := true
	for _, s := range view.Selection {
		included[s.NodeID] = s.State == "included"
		if s.State == "blocked" {
			complete = false
		}
	}
	view.Members = slices.Clone(view.Members)
	view.Members = slices.DeleteFunc(view.Members, func(m ConsistencyMember) bool { return !included[m.NodeID] })
	return view, complete
}
