package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EvidenceUseProjection is a historical, versioned readback. Consumers must use
// CheckCurrentEvidencePlan again at final acceptance, never trust a cached pass.
type EvidenceUseProjection struct {
	Snapshot   evidenceuse.Snapshot   `json:"snapshot"`
	Assessment evidenceuse.Assessment `json:"assessment"`
}

// loadEvidenceUseSnapshot reads the complete store within a hard resource bound.
// It deliberately fails closed instead of confusing display depth/RLS/limits
// with absence. Every caller supplies one RR cut or the common write guard.
func loadEvidenceUseSnapshot(ctx context.Context, tx sqlTx) (evidenceuse.Snapshot, error) {
	s := evidenceuse.Snapshot{Version: evidenceuse.ProjectionV1, Nodes: map[string]evidenceuse.Node{}, Sources: map[string]evidenceuse.Source{}, Contradictions: []evidenceuse.Contradiction{}}
	if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return s, err
	}
	var revision int64
	if err := tx.queryRow(ctx, `SELECT store_id,revision FROM evidence_governance_head WHERE singleton`).Scan(&s.StoreID, &revision); err != nil {
		return s, err
	}
	s.CutID = fmt.Sprintf("governance:%d", revision)
	rows, err := tx.query(ctx, `SELECT n.canonical_node_id,n.node_kind,n.temporal,p.admission_outcome,coalesce(r.source_snapshot_id,''),coalesce(m.lineage_key,'')
 FROM canonical_graph_nodes n
 LEFT JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
 LEFT JOIN extraction_attempts a USING(extraction_attempt_id)
 LEFT JOIN extraction_runs r USING(extraction_run_id)
 LEFT JOIN canonical_supersession_members m ON m.canonical_node_id=n.canonical_node_id
 ORDER BY n.canonical_node_id LIMIT 10001`)
	if err != nil {
		return s, err
	}
	lineages := map[string]bool{}
	for rows.Next() {
		var id, kind, admission, source, lineage string
		var temporal []byte
		if err := rows.Scan(&id, &kind, &temporal, &admission, &source, &lineage); err != nil {
			rows.Close()
			return s, err
		}
		if _, ok := s.Nodes[id]; ok {
			rows.Close()
			return s, fmt.Errorf("ambiguous canonical lineage or source binding")
		}
		n := evidenceuse.Node{Kind: evidencegraph.CanonicalNodeKind(kind), Admission: admission, Withdrawal: evidenceuse.Withdrawal{Status: evidenceuse.WithdrawalNone, Complete: true}, Currentness: evidenceuse.Currentness{LineageKey: lineage, Complete: lineage == ""}, Parents: []string{}, SourceIDs: []string{}, SourcesComplete: source != "", ContradictionsComplete: true}
		n.ParentsComplete = n.Kind != evidencegraph.CanonicalDerivedClaim
		if err := json.Unmarshal(temporal, &n.Temporal); err != nil {
			rows.Close()
			return s, err
		}
		if source != "" {
			n.SourceIDs = []string{source}
			s.Sources[source] = evidenceuse.Source{SnapshotID: source}
		}
		if lineage != "" {
			lineages[lineage] = true
		}
		s.Nodes[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	if len(s.Nodes) > 10000 {
		return s, fmt.Errorf("governance node limit exceeded")
	}
	rows, err = tx.query(ctx, `SELECT d.node_id,p.parent_node_id,p.canonical_edge_id,e.from_node_id,e.to_node_id,e.relation FROM canonical_derivations d LEFT JOIN canonical_derivation_parents p USING(derivation_id) LEFT JOIN canonical_graph_edges e USING(canonical_edge_id) ORDER BY d.node_id,p.parent_node_id LIMIT 100001`)
	if err != nil {
		return s, err
	}
	count := 0
	for rows.Next() {
		var id, parent, edge, from, to, relation string
		if err := rows.Scan(&id, &parent, &edge, &from, &to, &relation); err != nil {
			rows.Close()
			return s, err
		}
		count++
		n, ok := s.Nodes[id]
		if !ok || from != parent || to != id || relation != string(evidencegraph.CanonicalDerivedFrom) {
			rows.Close()
			return s, fmt.Errorf("incomplete derivation authority")
		}
		n.Parents = append(n.Parents, parent)
		n.ParentsComplete = true
		s.Nodes[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	if count > 100000 {
		return s, fmt.Errorf("governance parent limit exceeded")
	}
	rows, err = tx.query(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,provenance FROM canonical_graph_edges WHERE relation='contradicts' ORDER BY canonical_edge_id LIMIT 100001`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var e evidenceuse.Contradiction
		var raw []byte
		if err := rows.Scan(&e.ID, &e.Left, &e.Right, &raw); err != nil {
			rows.Close()
			return s, err
		}
		var p evidencegraph.ProvenanceRecord
		if err := json.Unmarshal(raw, &p); err != nil {
			rows.Close()
			return s, err
		}
		e.ReviewRef = p.ID
		e.Scope = "recorded_explicit_pair"
		s.Contradictions = append(s.Contradictions, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	if len(s.Contradictions) > 100000 {
		return s, fmt.Errorf("governance contradiction limit exceeded")
	}
	var jointAuthority *evidencesupersession.JointProjector
	if len(lineages) > 0 {
		hasRegistration, err := hasInitialLineageRegistrations(ctx, tx)
		if err != nil {
			return s, err
		}
		if hasRegistration {
			authority, err := loadJointLineageAuthority(ctx, tx)
			if err != nil {
				return s, err
			}
			jointAuthority, err = evidencesupersession.PrepareJointAuthority(authority)
			if err != nil {
				return s, err
			}
		}
	}
	for key := range lineages {
		var nodes []evidencesupersession.NodeCurrentness
		var complete bool
		witnessID := ""
		if jointAuthority != nil {
			input, err := loadJointLineageMaterialization(ctx, tx, key)
			if err != nil {
				return s, err
			}
			p, err := jointAuthority.ProjectLineage(input)
			if err != nil {
				return s, err
			}
			nodes = p.Nodes
			complete = p.ClosureAvailable
			if p.Witness != nil {
				witnessID = p.Witness.ID
			}
		} else {
			input, err := loadCanonicalSupersessionClosureInput(ctx, tx, key)
			if err != nil {
				return s, err
			}
			p, err := evidencesupersession.ProjectCurrentness(input)
			if err != nil {
				return s, err
			}
			nodes = p.Nodes
			complete = p.ClosureAvailable
			if p.Witness != nil {
				witnessID = p.Witness.ID
			}
		}
		for _, state := range nodes {
			n, ok := s.Nodes[state.NodeID]
			if !ok {
				return s, fmt.Errorf("missing lineage member")
			}
			n.Currentness.Status = state.Status
			n.Currentness.Complete = complete
			n.Currentness.WitnessID = witnessID
			s.Nodes[state.NodeID] = n
		}
	}
	for id := range s.Sources {
		source, err := loadGovernanceSourceState(ctx, tx, id)
		if err != nil {
			return s, err
		}
		s.Sources[id] = source
	}

	for id, n := range s.Nodes {
		events, err := loadGovernanceEvents(ctx, tx, "withdrawal", id)
		if err != nil {
			return s, err
		}
		if len(events) > 0 {
			e := events[len(events)-1]
			status := evidenceuse.WithdrawalWithdrawn
			if e.Display.Request.Operation == "restore" {
				status = evidenceuse.WithdrawalRestored
			}
			n.Withdrawal = evidenceuse.Withdrawal{Status: status, Complete: true, Revision: e.Revision, EventID: e.EventID, Reason: e.Reason, CorrectionIDs: e.Display.Request.CorrectionIDs}
			s.Nodes[id] = n
		}
	}
	if _, err := s.Digest(); err != nil {
		return s, err
	}
	return s, nil
}
func loadEvidenceUseProjection(ctx context.Context, tx sqlTx, ids []string) (EvidenceUseProjection, error) {
	return loadEvidenceUseProjectionForPolicy(ctx, tx, evidenceuse.CurrentActionV1, ids)
}
func loadEvidenceUseProjectionForPolicy(ctx context.Context, tx sqlTx, policy evidenceuse.Policy, ids []string) (EvidenceUseProjection, error) {
	if err := evidenceuse.ValidatePolicy(policy); err != nil {
		return EvidenceUseProjection{}, err
	}
	s, err := loadEvidenceUseSnapshot(ctx, tx)
	if err != nil {
		return EvidenceUseProjection{}, err
	}
	a, err := evidenceuse.Assess(s, policy, ids, time.Now().UTC())
	return EvidenceUseProjection{Snapshot: s, Assessment: a}, err
}
func ReadEvidenceUseProjection(ctx context.Context, pool *pgxpool.Pool, ids []string) (EvidenceUseProjection, error) {
	return ReadEvidenceUseProjectionForPolicy(ctx, pool, evidenceuse.CurrentActionV1, ids)
}

// ReadEvidenceUseProjectionForPolicy evaluates the explicitly selected purpose
// against one authoritative snapshot. It is not a permit for later execution.
func ReadEvidenceUseProjectionForPolicy(ctx context.Context, pool *pgxpool.Pool, policy evidenceuse.Policy, ids []string) (EvidenceUseProjection, error) {
	if err := evidenceuse.ValidatePolicy(policy); err != nil {
		return EvidenceUseProjection{}, err
	}
	if pool == nil {
		return EvidenceUseProjection{}, fmt.Errorf("postgres pool required")
	}
	var p EvidenceUseProjection
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		p, err = loadEvidenceUseProjectionForPolicy(ctx, tx, policy, ids)
		return err
	})
	return p, err
}

// CheckCurrentEvidencePlan is the deterministic final action-to-basis gate. It
// rereads authoritative state even after a planning assessment or SAT result.
// The purpose is explicit; a historical label cannot bypass the gate.
func CheckCurrentEvidencePlan(ctx context.Context, pool *pgxpool.Pool, policy evidenceuse.Policy, actions []evidenceuse.ActionBasis) (evidenceuse.PlanAssessment, error) {
	if err := evidenceuse.ValidatePolicy(policy); err != nil {
		return evidenceuse.PlanAssessment{}, err
	}
	if pool == nil {
		return evidenceuse.PlanAssessment{}, fmt.Errorf("postgres pool required")
	}
	var p evidenceuse.PlanAssessment
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		s, err := loadEvidenceUseSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		p, err = evidenceuse.CheckPlan(s, policy, actions, time.Now().UTC())
		return err
	})
	return p, err
}
func guardDerivedCurrentUse(ctx context.Context, cut *endpointUseCut, mutation canonicalAdmissionMutation, policy evidenceuse.Policy) error {
	tx := cut.tx
	if err := lockGovernance(ctx, tx); err != nil {
		return err
	}
	s, err := cut.snapshot(ctx)
	if err != nil {
		return err
	}
	// Add the candidate only to this assessment, not the shared native snapshot.
	s.Nodes = maps.Clone(s.Nodes)
	s.Sources = maps.Clone(s.Sources)
	node := mutation.nodes[0]
	var sid string
	if err := tx.queryRow(ctx, `SELECT r.source_snapshot_id FROM proposal_occurrences p JOIN extraction_attempts a USING(extraction_attempt_id) JOIN extraction_runs r USING(extraction_run_id) WHERE p.proposal_occurrence_id=$1`, node.OriginProposalOccurrenceID).Scan(&sid); err != nil {
		return err
	}
	source, err := loadGovernanceSourceState(ctx, tx, sid)
	if err != nil {
		return err
	}
	s.Sources[sid] = source
	s.Nodes[node.ID] = evidenceuse.Node{Kind: node.Kind, Admission: "admitted", Withdrawal: evidenceuse.Withdrawal{Status: evidenceuse.WithdrawalNone, Complete: true}, Currentness: evidenceuse.Currentness{Complete: true}, Temporal: node.Temporal, Parents: mutation.derivation.Parents, ParentsComplete: true, SourceIDs: []string{sid}, SourcesComplete: true, ContradictionsComplete: true}
	a, err := evidenceuse.Assess(s, policy, []string{node.ID}, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, n := range a.Nodes {
		if n.NodeID == node.ID && n.Outcome != evidenceuse.Eligible {
			return fmt.Errorf("current-action governance rejected basis: %v (not a SAT result)", a.Nodes)
		}
	}
	return nil
}

// GetCanonicalEvidenceStateByID keeps original admission and current governance
// in the same RR transaction. Internal historical review uses the raw reader.
func GetCanonicalEvidenceStateByID(ctx context.Context, pool *pgxpool.Pool, id string) (CanonicalQueryResult, error) {
	if pool == nil {
		return CanonicalQueryResult{}, fmt.Errorf("postgres pool required")
	}
	var out CanonicalQueryResult
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		out, err = getCanonicalEvidenceByID(ctx, tx, id)
		if err != nil {
			return err
		}
		p, err := loadEvidenceUseProjection(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		out.UseState = &p
		return nil
	})
	return out, err
}

// TraceProposalEvidenceState keeps an admitted occurrence's provenance and
// canonical governance in the same RR cut. Pending and relation occurrences
// remain proposal records; a missing canonical node cannot become clearance.
func TraceProposalEvidenceState(ctx context.Context, pool *pgxpool.Pool, id string) (ProposalQueryResult, error) {
	if pool == nil {
		return ProposalQueryResult{}, fmt.Errorf("postgres pool required")
	}
	var out ProposalQueryResult
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		out, err = traceProposalProvenance(ctx, tx, id)
		if err != nil {
			return err
		}
		if out.AdmissionOutcome == admissionOutcomeAdmitted && strings.HasPrefix(out.CanonicalRef, "canon-node:") {
			p, err := loadEvidenceUseProjection(ctx, tx, []string{out.CanonicalRef})
			if err != nil {
				return err
			}
			out.UseState = &p
		}
		return nil
	})
	return out, err
}

// EndpointUseReview binds scoped state and policy reasons, not the global head
// or wall clock. Unrelated writes therefore need not invalidate human approval.
// This is review material, not a reusable Snapshot/acceptance permit.
type EndpointUseReview struct {
	Policy         evidenceuse.Policy            `json:"policy"`
	StoreID        string                        `json:"store_id"`
	States         map[string]evidenceuse.Node   `json:"states"`
	Sources        map[string]evidenceuse.Source `json:"sources"`
	Contradictions []evidenceuse.Contradiction   `json:"contradictions"`
	Decisions      []evidenceuse.NodeAssessment  `json:"decisions"`
}

func loadEndpointUseReview(ctx context.Context, cut *endpointUseCut, policy evidenceuse.Policy, roots []string) (*EndpointUseReview, error) {
	snapshot, err := cut.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	assessment, err := evidenceuse.Assess(snapshot, policy, roots, time.Now().UTC())
	p := EvidenceUseProjection{Snapshot: snapshot, Assessment: assessment}
	if err != nil {
		return nil, err
	}
	r := &EndpointUseReview{Policy: policy, StoreID: p.Snapshot.StoreID, States: map[string]evidenceuse.Node{}, Sources: map[string]evidenceuse.Source{}, Contradictions: []evidenceuse.Contradiction{}, Decisions: p.Assessment.Nodes}
	selected := map[string]bool{}
	for _, n := range p.Assessment.Nodes {
		selected[n.NodeID] = true
	}
	for _, e := range p.Snapshot.Contradictions {
		if selected[e.Left] || selected[e.Right] {
			r.Contradictions = append(r.Contradictions, e)
		}
	}
	for _, e := range r.Contradictions {
		selected[e.Left], selected[e.Right] = true, true
	}
	for id := range selected {
		n, ok := p.Snapshot.Nodes[id]
		if !ok {
			continue
		}
		n.Currentness.WitnessID = ""
		r.States[id] = n
		for _, sid := range n.SourceIDs {
			r.Sources[sid] = p.Snapshot.Sources[sid]
		}
	}
	return r, nil
}

func loadGovernanceSourceState(ctx context.Context, tx sqlQueryer, id string) (evidenceuse.Source, error) {
	var source evidenceuse.Source
	var system string
	var meta []byte
	err := tx.queryRow(ctx, `SELECT source_snapshot_id,raw_content_hash,source_version,origin_metadata,source_system FROM source_snapshots WHERE source_snapshot_id=$1`, id).Scan(&source.SnapshotID, &source.ContentHash, &source.ProviderRevision, &meta, &system)
	if err != nil {
		return source, err
	}
	var declarations map[string]string
	if err := json.Unmarshal(meta, &declarations); err != nil {
		return source, err
	}
	source.DeclaredFidelity, source.DeclaredCoverage = declarations["external_content_fidelity"], declarations["external_coverage"]
	// External source_version is an intake identity; only the original external
	// provider revision can support a historical correspondence claim.
	if system == SourceSystemExternalDocument {
		source.ProviderRevision = declarations["external_revision"]
	}
	events, err := loadGovernanceEvents(ctx, tx, "source_assessment", id)
	if err != nil {
		return source, err
	}
	resolved := map[string]bool{}
	for _, e := range events {
		for _, old := range e.Display.Request.ResolvesEventIDs {
			resolved[old] = true
		}
		source.AssessmentRevision = e.Revision
	}
	source.AssessmentsComplete = true
	source.Checks = []evidenceuse.SourceCheck{}
	for _, e := range events {
		if !resolved[e.EventID] {
			for _, c := range e.Display.Request.Checks {
				c.AssessmentID = e.EventID
				source.Checks = append(source.Checks, c)
			}
		}
	}
	return source, nil
}
