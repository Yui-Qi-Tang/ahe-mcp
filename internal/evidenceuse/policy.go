package evidenceuse

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
)

type Policy string

const (
	// CurrentActionV1 requires complete recorded governance and explicit
	// currentness. Positive source verification is optional; existing failures
	// and unresolved findings still block use. It makes no fidelity claim.
	CurrentActionV1 Policy = "current-action/v1"
	// GroundedActionV1 additionally requires revision-bound reviewed byte,
	// correspondence and coverage checks for every source in the AND ancestry.
	GroundedActionV1 Policy = "grounded-current-action/v1"
)

// ValidatePolicy rejects absent, unknown and historical policies at current-use
// boundaries. A caller must select the purpose before requesting clearance.
func ValidatePolicy(policy Policy) error {
	if policy != CurrentActionV1 && policy != GroundedActionV1 {
		return fmt.Errorf("%w: unsupported use policy %q", ErrInvalidProjection, policy)
	}
	return nil
}

type Outcome string

const (
	Eligible Outcome = "eligible"
	Blocked  Outcome = "blocked"
	Excluded Outcome = "excluded"
)

type Reason struct {
	Code      string `json:"code"`
	RelatedID string `json:"related_id,omitempty"`
}

type NodeAssessment struct {
	NodeID  string   `json:"node_id"`
	Outcome Outcome  `json:"outcome"`
	Reasons []Reason `json:"reasons"`
}

// Assessment describes one cut at one evaluation time. It has no permission or
// solver-result field: callers must not substitute SAT for this basis check.
type Assessment struct {
	ProjectionVersion string           `json:"projection_version"`
	StateDigest       string           `json:"state_digest"`
	Policy            Policy           `json:"policy"`
	EvaluatedAt       time.Time        `json:"evaluated_at"`
	Roots             []string         `json:"roots"`
	Nodes             []NodeAssessment `json:"nodes"`
}

// Assess evaluates roots and their complete immutable AND ancestry. Missing
// evidence blocks; malformed or cyclic input returns no usable assessment.
// Conflict activity uses explicit peer state before final eligibility, so two
// blocked endpoints cannot make their own contradiction disappear.
func Assess(s Snapshot, policy Policy, roots []string, now time.Time) (Assessment, error) {
	if err := ValidatePolicy(policy); err != nil {
		return Assessment{}, err
	}
	if now.IsZero() || len(roots) == 0 || len(s.Nodes) > 10000 {
		return Assessment{}, fmt.Errorf("%w: evaluation time, roots and bounded input required", ErrInvalidProjection)
	}
	roots = sortedCopy(roots)
	if hasDuplicates(roots) {
		return Assessment{}, fmt.Errorf("%w: duplicate or empty roots", ErrInvalidProjection)
	}
	digest, err := s.Digest()
	if err != nil {
		return Assessment{}, err
	}
	edges := map[string][]Contradiction{}
	for _, edge := range s.Contradictions {
		edges[edge.Left] = append(edges[edge.Left], edge)
		edges[edge.Right] = append(edges[edge.Right], edge)
	}
	done := map[string]NodeAssessment{}
	visiting := map[string]bool{}
	var visit func(string) (NodeAssessment, error)
	visit = func(id string) (NodeAssessment, error) {
		if a, ok := done[id]; ok {
			return a, nil
		}
		if visiting[id] {
			return NodeAssessment{}, fmt.Errorf("%w: cyclic AND ancestry at %s", ErrInvalidProjection, id)
		}
		a := NodeAssessment{NodeID: id, Outcome: Eligible, Reasons: []Reason{}}
		n, ok := s.Nodes[id]
		if !ok {
			a.add(Blocked, "missing_node", id)
			done[id] = a
			return a, nil
		}
		visiting[id] = true
		defer delete(visiting, id)
		if n.Admission != "admitted" {
			a.add(Blocked, "admission_not_confirmed", id)
		}
		if n.Kind != evidencegraph.CanonicalSourceClaim && n.Kind != evidencegraph.CanonicalDerivedClaim {
			a.add(Blocked, "unsupported_node_kind", id)
		}
		state, reasons := explicitState(n, now)
		for _, reason := range reasons {
			a.add(state, reason, id)
		}
		if !n.ParentsComplete {
			a.add(Blocked, "incomplete_parent_manifest", id)
		}
		if n.Kind == evidencegraph.CanonicalDerivedClaim && len(n.Parents) == 0 {
			a.add(Blocked, "missing_and_parents", id)
		}
		if n.Kind == evidencegraph.CanonicalSourceClaim && len(n.Parents) != 0 {
			return NodeAssessment{}, fmt.Errorf("%w: source claim has derivation parents", ErrInvalidProjection)
		}
		for _, parent := range sortedCopy(n.Parents) {
			p, err := visit(parent)
			if err != nil {
				return NodeAssessment{}, err
			}
			if p.Outcome != Eligible {
				a.add(p.Outcome, "ineligible_parent", parent)
			}
		}
		if !n.SourcesComplete {
			a.add(Blocked, "incomplete_source_bindings", id)
		}
		if n.Kind == evidencegraph.CanonicalSourceClaim && len(n.SourceIDs) == 0 {
			a.add(Blocked, "missing_source_basis", id)
		}
		for _, sourceID := range sortedCopy(n.SourceIDs) {
			source, ok := s.Sources[sourceID]
			if !ok {
				a.add(Blocked, "missing_source", sourceID)
				continue
			}
			if !source.AssessmentsComplete {
				a.add(Blocked, "incomplete_source_assessments", sourceID)
			}
			for _, criterion := range []string{RetentionV1, CorrespondenceV1, CoverageV1} {
				// Optional positive verification does not erase an existing
				// negative or unresolved finding. Only a complete absence of
				// findings can skip a criterion under the weaker policy.
				if policy == CurrentActionV1 && !slices.ContainsFunc(source.Checks, func(c SourceCheck) bool { return c.Criterion == criterion }) {
					continue
				}
				result := sourceCriterion(source, criterion)
				if result != Eligible {
					a.add(result, "source_check:"+criterion, sourceID)
				}
			}
		}
		if !n.ContradictionsComplete {
			a.add(Blocked, "incomplete_incident_contradictions", id)
		}
		for _, edge := range edges[id] {
			peerID := edge.Left
			if peerID == id {
				peerID = edge.Right
			}
			peer, ok := s.Nodes[peerID]
			if !ok {
				a.add(Blocked, "unknown_conflict_peer", edge.ID)
				continue
			}
			peerState, _ := explicitState(peer, now)
			if peer.Admission != "admitted" || peerState == Blocked {
				a.add(Blocked, "unknown_conflict_peer", edge.ID)
			} else if peerState == Eligible {
				a.add(Blocked, "active_contradiction", edge.ID)
			}
		}
		slices.SortFunc(a.Reasons, func(a, b Reason) int {
			if c := cmp.Compare(a.Code, b.Code); c != 0 {
				return c
			}
			return cmp.Compare(a.RelatedID, b.RelatedID)
		})
		done[id] = a
		return a, nil
	}
	for _, id := range roots {
		if _, err := visit(id); err != nil {
			return Assessment{}, err
		}
	}
	out := Assessment{ProjectionVersion: ProjectionV1, StateDigest: digest, Policy: policy, EvaluatedAt: now.UTC(), Roots: roots, Nodes: []NodeAssessment{}}
	for _, a := range done {
		out.Nodes = append(out.Nodes, a)
	}
	slices.SortFunc(out.Nodes, func(a, b NodeAssessment) int {
		return cmp.Compare(a.NodeID, b.NodeID)
	})
	return out, nil
}

func (a *NodeAssessment) add(outcome Outcome, code, related string) {
	// A known exclusion remains an exclusion even when another required
	// dimension is unknown; preserve both witnesses without order dependence.
	if outcome == Excluded || (outcome == Blocked && a.Outcome == Eligible) {
		a.Outcome = outcome
	}
	a.Reasons = append(a.Reasons, Reason{Code: code, RelatedID: related})
}

func explicitState(n Node, now time.Time) (Outcome, []string) {
	a := NodeAssessment{Outcome: Eligible}
	w := n.Withdrawal
	switch {
	case w.Revision < 0 || (w.Status == WithdrawalNone && (w.Revision != 0 || w.EventID != "")):
		a.add(Blocked, "invalid_withdrawal_coordinate", "")
	case (w.Status == WithdrawalWithdrawn || w.Status == WithdrawalRestored) && (w.Revision == 0 || !validText(w.EventID)):
		a.add(Blocked, "invalid_withdrawal_coordinate", "")
	case w.Status == WithdrawalWithdrawn:
		a.add(Excluded, "withdrawn", "")
	case !w.Complete || (w.Status != WithdrawalNone && w.Status != WithdrawalRestored):
		a.add(Blocked, "unknown_withdrawal", "")
	}
	valid, reason := validity(n.Temporal, now)
	if !valid {
		a.add(Blocked, reason, "")
	} else if reason != "" {
		a.add(Excluded, reason, "")
	}
	switch {
	case n.Currentness.Status == evidencesupersession.CurrentnessSuperseded:
		a.add(Excluded, "superseded", "")
	case n.Temporal.Status == evidencegraph.TemporalStale || n.Temporal.Status == evidencegraph.TemporalSuperseded:
		a.add(Excluded, "recorded_"+string(n.Temporal.Status), "")
	case !n.Currentness.Complete:
		a.add(Blocked, "incomplete_currentness", "")
	case n.Currentness.LineageKey != "":
		if n.Currentness.Status != evidencesupersession.CurrentnessCurrent || !validText(n.Currentness.WitnessID) {
			a.add(Blocked, "unresolved_lineage_currentness", "")
		}
	case n.Currentness.Status != "" && n.Currentness.Status != evidencesupersession.CurrentnessUnknown:
		a.add(Blocked, "currentness_without_lineage", "")
	case n.Kind == evidencegraph.CanonicalDerivedClaim && n.Temporal.Status == evidencegraph.TemporalUnknown:
		// Derived current use is computed from the complete AND ancestry.
		// Unknown source currentness never receives this exception.
	case n.Temporal.Status != evidencegraph.TemporalCurrent:
		a.add(Blocked, "unclassified_currentness", "")
	}
	reasons := make([]string, len(a.Reasons))
	for i, reason := range a.Reasons {
		reasons[i] = reason.Code
	}
	return a.Outcome, reasons
}

func sourceCriterion(source Source, criterion string) Outcome {
	pass, fail, unknown := false, false, false
	for _, check := range source.Checks {
		if check.Criterion != criterion {
			continue
		}
		if check.SourceSnapshotID != source.SnapshotID || check.ContentHash != source.ContentHash || check.Scope != RetainedSnapshotScope || !validText(check.MaterialRef) || (criterion != RetentionV1 && (source.ProviderRevision == "" || check.ComparedRevision != source.ProviderRevision)) {
			unknown = true
			continue
		}
		switch check.Result {
		case CheckPass:
			pass = true
		case CheckFail:
			fail = true
		default:
			unknown = true
		}
	}
	if unknown || (pass && fail) {
		return Blocked
	}
	if fail {
		return Excluded
	}
	if pass {
		return Eligible
	}
	return Blocked
}

// ActionBasis contains only the producer's declared premises. Validating it
// cannot prove semantic entailment or discover premises the producer omitted.
type ActionBasis struct {
	ActionID string   `json:"action_id"`
	BasisIDs []string `json:"basis_ids"`
}

type PlanAssessment struct {
	Assessment Assessment    `json:"assessment"`
	Actions    []ActionBasis `json:"actions"`
	Accepted   bool          `json:"accepted"`
}

// CheckPlan recomputes the basis gate at acceptance time, even after SAT or a
// previous planning check. Historical receipts/assessments are not inputs.
func CheckPlan(s Snapshot, policy Policy, actions []ActionBasis, now time.Time) (PlanAssessment, error) {
	if len(actions) == 0 {
		return PlanAssessment{}, fmt.Errorf("%w: empty action manifest", ErrInvalidProjection)
	}
	seenActions, roots := map[string]bool{}, map[string]bool{}
	manifest := make([]ActionBasis, len(actions))
	for i, action := range actions {
		if !validText(action.ActionID) || seenActions[action.ActionID] || len(action.BasisIDs) == 0 {
			return PlanAssessment{}, fmt.Errorf("%w: actions require distinct IDs and nonempty bases", ErrInvalidProjection)
		}
		seenActions[action.ActionID] = true
		action.BasisIDs = sortedCopy(action.BasisIDs)
		if hasDuplicates(action.BasisIDs) {
			return PlanAssessment{}, fmt.Errorf("%w: duplicate or empty action bases", ErrInvalidProjection)
		}
		for _, id := range action.BasisIDs {
			roots[id] = true
		}
		manifest[i] = action
	}
	ids := make([]string, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	assessment, err := Assess(s, policy, ids, now)
	if err != nil {
		return PlanAssessment{}, err
	}
	accepted := true
	for _, node := range assessment.Nodes {
		if roots[node.NodeID] && node.Outcome != Eligible {
			accepted = false
		}
	}
	return PlanAssessment{Assessment: assessment, Actions: manifest, Accepted: accepted}, nil
}
