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

// Governance scope bounds limit one evaluation scope, not the store.
const (
	governanceScopeMaxNodes          = 10000
	governanceScopeMaxParents        = 100000
	governanceScopeMaxContradictions = 100000
)

// loadEvidenceUseSnapshot reads the governance state needed to assess roots:
// the roots, their complete AND ancestry, every contradiction incident to that
// closure, each conflict peer's own state, and the sources, lineages and
// withdrawals of those nodes. Nodes outside that scope are not read, so store
// size does not bound the read. It fails closed instead of confusing display
// depth/RLS/limits with absence. Every caller supplies one RR cut or the common
// write guard.
func loadEvidenceUseSnapshot(ctx context.Context, tx sqlTx, roots []string) (evidenceuse.Snapshot, error) {
	s := evidenceuse.Snapshot{Version: evidenceuse.ProjectionV2, Nodes: map[string]evidenceuse.Node{}, Sources: map[string]evidenceuse.Source{}, Contradictions: []evidenceuse.Contradiction{}}
	if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return s, err
	}
	var revision int64
	if err := tx.queryRow(ctx, `SELECT store_id,revision FROM evidence_governance_head WHERE singleton`).Scan(&s.StoreID, &revision); err != nil {
		return s, err
	}
	s.CutID = fmt.Sprintf("governance:%d", revision)
	parents := 0
	fetchNodes := func(ctx context.Context, ids []string) (map[string]evidenceuse.Node, error) {
		return loadGovernanceScopeNodes(ctx, tx, ids, &parents)
	}
	fetchContradictions := func(ctx context.Context, ids []string) ([]evidenceuse.Contradiction, error) {
		return loadGovernanceScopeContradictions(ctx, tx, ids)
	}
	nodes, contradictions, err := collectEvidenceUseScope(ctx, roots, fetchNodes, fetchContradictions)
	if err != nil {
		return s, err
	}
	s.Nodes, s.Contradictions = nodes, contradictions
	lineages := map[string]bool{}
	for _, n := range s.Nodes {
		for _, source := range n.SourceIDs {
			s.Sources[source] = evidenceuse.Source{SnapshotID: source}
		}
		if n.Currentness.LineageKey != "" {
			lineages[n.Currentness.LineageKey] = true
		}
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
				// The lineage projection covers all members; only scoped
				// members are part of this snapshot.
				continue
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
	s, err := loadEvidenceUseSnapshot(ctx, tx, ids)
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
		var roots []string
		for _, action := range actions {
			roots = append(roots, action.BasisIDs...)
		}
		s, err := loadEvidenceUseSnapshot(ctx, tx, roots)
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
	s, err := cut.snapshot(ctx, mutation.derivation.Parents)
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
	useClass := evidencegraph.DerivationOrdinary
	if mutation.derivation.UseClass != "" {
		useClass = mutation.derivation.UseClass
	}
	s.Nodes[node.ID] = evidenceuse.Node{UseClass: useClass, Kind: node.Kind, Admission: "admitted", Withdrawal: evidenceuse.Withdrawal{Status: evidenceuse.WithdrawalNone, Complete: true}, Currentness: evidenceuse.Currentness{Complete: true}, Temporal: node.Temporal, Parents: mutation.derivation.Parents, ParentsComplete: true, SourceIDs: []string{sid}, SourcesComplete: true, ContradictionsComplete: true}
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
	snapshot, err := cut.snapshot(ctx, roots)
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
		// Preserve the v1 ordinary review wire shape. The live v2 snapshot still
		// binds the explicit class; historical restrictions remain visible here.
		if n.UseClass == evidencegraph.DerivationOrdinary {
			n.UseClass = ""
		}
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
	// Preserve the original external revision declaration, regardless of issuer.
	// An internal source_version cannot fill a missing historical declaration.
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
