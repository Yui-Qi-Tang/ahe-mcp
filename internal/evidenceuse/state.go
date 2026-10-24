// Package evidenceuse evaluates declared evidence bases under explicit use
// policies. It does not admit evidence, verify source truth, or grant an
// execution permit. A caller must load one authoritative database cut and
// repeat the assessment under the write guard before accepting a new derivation.
package evidenceuse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
)

const ProjectionV1 = "evidence-use-projection/v1"

var ErrInvalidProjection = errors.New("invalid evidence use projection")

// Snapshot is an internal adapter contract, not a caller-authorized MCP input.
// Complete fields must come from authoritative coverage checks, including rows
// outside any displayed graph. An RLS-filtered or truncated read cannot assert
// completeness. The digest binds this transport; it does not prove its origin.
type Snapshot struct {
	Version        string            `json:"version"`
	StoreID        string            `json:"store_id"`
	CutID          string            `json:"cut_id"`
	Nodes          map[string]Node   `json:"nodes"`
	Sources        map[string]Source `json:"sources"`
	Contradictions []Contradiction   `json:"contradictions"`
}

// Node keeps admission, withdrawal, currentness and AND dependencies separate.
// SourceIDs are actual native bindings, never matches on object_id or text.
type Node struct {
	Kind                   evidencegraph.CanonicalNodeKind `json:"kind"`
	Admission              string                          `json:"admission"`
	Withdrawal             Withdrawal                      `json:"withdrawal"`
	Currentness            Currentness                     `json:"currentness"`
	Temporal               evidencegraph.TemporalRecord    `json:"temporal"`
	Parents                []string                        `json:"parents"`
	ParentsComplete        bool                            `json:"parents_complete"`
	SourceIDs              []string                        `json:"source_ids"`
	SourcesComplete        bool                            `json:"sources_complete"`
	ContradictionsComplete bool                            `json:"contradictions_complete"`
}

type WithdrawalStatus string

const (
	WithdrawalUnknown   WithdrawalStatus = "unknown"
	WithdrawalNone      WithdrawalStatus = "none"
	WithdrawalWithdrawn WithdrawalStatus = "withdrawn"
	WithdrawalRestored  WithdrawalStatus = "restored"
)

// Withdrawal describes only the stop-use decision. Restored does not alter any
// other dimension, and correction references never become replacement parents.
type Withdrawal struct {
	Status        WithdrawalStatus `json:"status"`
	Complete      bool             `json:"complete"`
	Revision      int64            `json:"revision"`
	EventID       string           `json:"event_id,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	CorrectionIDs []string         `json:"correction_ids,omitempty"`
}

type Currentness struct {
	LineageKey string                                 `json:"lineage_key,omitempty"`
	Status     evidencesupersession.CurrentnessStatus `json:"status"`
	Complete   bool                                   `json:"complete"`
	WitnessID  string                                 `json:"witness_id,omitempty"`
}

type CheckResult string

const (
	CheckUnknown      CheckResult = "unknown"
	CheckPass         CheckResult = "pass"
	CheckFail         CheckResult = "fail"
	CheckInconclusive CheckResult = "inconclusive"
)

const (
	RetentionV1 = "retained-byte-integrity/v1"
	// CorrespondenceV1 keeps its wire name; it assesses source fidelity against
	// comparison materials, not who issued the externally assigned revision.
	CorrespondenceV1      = "provider-revision-correspondence/v1"
	CoverageV1            = "declared-source-coverage/v1"
	RetainedSnapshotScope = "entire_retained_snapshot"
)

// Source preserves original declarations separately from scoped review findings.
// Checks contains unresolved applicable findings, plus any explicit reviewed
// resolutions supplied by the adapter. The adapter must not choose a finding
// by timestamp. Statement-support findings cannot stand in for these checks.
// ProviderRevision is the original source revision declaration. Its legacy JSON
// name is retained for compatibility and does not require provider issuance.
type Source struct {
	SnapshotID          string        `json:"source_snapshot_id"`
	ContentHash         string        `json:"content_hash"`
	ProviderRevision    string        `json:"provider_revision"`
	DeclaredFidelity    string        `json:"declared_fidelity"`
	DeclaredCoverage    string        `json:"declared_coverage"`
	AssessmentRevision  int64         `json:"assessment_revision"`
	AssessmentsComplete bool          `json:"assessments_complete"`
	Checks              []SourceCheck `json:"checks"`
}

// SourceCheck is a reviewed finding about the exact snapshot and original
// externally assigned source revision. A comparison only with today's code has a different
// revision and cannot establish historical source correspondence.
type SourceCheck struct {
	AssessedField    string      `json:"assessed_field,omitempty"`
	CorrectedValue   string      `json:"corrected_value,omitempty"`
	AssessmentID     string      `json:"assessment_id"`
	SourceSnapshotID string      `json:"source_snapshot_id"`
	ContentHash      string      `json:"content_hash"`
	Scope            string      `json:"scope"`
	Criterion        string      `json:"criterion"`
	Result           CheckResult `json:"result"`
	ComparedRevision string      `json:"compared_revision"`
	MaterialRef      string      `json:"material_ref"`
	Limitations      []string    `json:"limitations"`
}

// Contradiction is one explicit reviewed pair. Scope is retained for inspection;
// v1 conservatively blocks recorded conflicts without inferring scope entailment.
// Connected components never supply additional pairwise edges.
type Contradiction struct {
	ID        string `json:"edge_id"`
	Left      string `json:"left"`
	Right     string `json:"right"`
	ReviewRef string `json:"review_ref"`
	Scope     string `json:"scope"`
}

func validText(s string) bool { return s != "" && s == strings.TrimSpace(s) }

// Digest binds graph-independent governance changes as well as claim bindings.
// Historical digests remain valid descriptions, never current-use clearances.
func (s Snapshot) Digest() (string, error) {
	if s.Version != ProjectionV1 || !validText(s.StoreID) || !validText(s.CutID) {
		return "", fmt.Errorf("%w: version, store and cut are required", ErrInvalidProjection)
	}
	// JSON map keys are sorted by encoding/json. Clone before sorting sets so
	// callers can safely retain their original snapshot and historical views.
	nodes := make(map[string]Node, len(s.Nodes))
	for id, n := range s.Nodes {
		if !validText(id) {
			return "", fmt.Errorf("%w: empty node ID", ErrInvalidProjection)
		}
		n.Parents = sortedCopy(n.Parents)
		n.SourceIDs = sortedCopy(n.SourceIDs)
		n.Withdrawal.CorrectionIDs = sortedCopy(n.Withdrawal.CorrectionIDs)
		if hasDuplicates(n.Parents) || hasDuplicates(n.SourceIDs) || hasDuplicates(n.Withdrawal.CorrectionIDs) {
			return "", fmt.Errorf("%w: duplicate bindings on %s", ErrInvalidProjection, id)
		}
		nodes[id] = n
	}
	s.Nodes = nodes
	sources := make(map[string]Source, len(s.Sources))
	for id, source := range s.Sources {
		if !validText(id) || source.SnapshotID != id || !validText(source.ContentHash) || source.AssessmentRevision < 0 {
			return "", fmt.Errorf("%w: invalid source coordinates", ErrInvalidProjection)
		}
		if len(source.Checks) > 0 && source.AssessmentRevision == 0 {
			return "", fmt.Errorf("%w: findings require a source assessment revision", ErrInvalidProjection)
		}
		source.Checks = slices.Clone(source.Checks)
		slices.SortFunc(source.Checks, func(a, b SourceCheck) int {
			if a.AssessmentID != b.AssessmentID {
				return strings.Compare(a.AssessmentID, b.AssessmentID)
			}
			return strings.Compare(a.Criterion, b.Criterion)
		})
		for i, check := range source.Checks {
			if !validText(check.AssessmentID) || !validText(check.Criterion) || (i > 0 && source.Checks[i-1].AssessmentID == check.AssessmentID && source.Checks[i-1].Criterion == check.Criterion) {
				return "", fmt.Errorf("%w: missing or duplicate source assessment ID", ErrInvalidProjection)
			}
		}
		sources[id] = source
	}
	s.Sources = sources
	s.Contradictions = slices.Clone(s.Contradictions)
	for i := range s.Contradictions {
		e := &s.Contradictions[i]
		if !validText(e.ID) || !validText(e.Left) || !validText(e.Right) || e.Left == e.Right || !validText(e.ReviewRef) {
			return "", fmt.Errorf("%w: invalid reviewed contradiction", ErrInvalidProjection)
		}
		if e.Left > e.Right {
			e.Left, e.Right = e.Right, e.Left
		}
	}
	slices.SortFunc(s.Contradictions, func(a, b Contradiction) int { return strings.Compare(a.ID, b.ID) })
	for i := 1; i < len(s.Contradictions); i++ {
		if s.Contradictions[i-1].ID == s.Contradictions[i].ID {
			return "", fmt.Errorf("%w: duplicate contradiction ID", ErrInvalidProjection)
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidProjection, err)
	}
	digest := sha256.Sum256(raw)
	return "evidence-use:sha256:" + hex.EncodeToString(digest[:]), nil
}

func sortedCopy(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return out
}

func hasDuplicates(values []string) bool {
	for i, value := range values {
		if !validText(value) || (i > 0 && values[i-1] == value) {
			return true
		}
	}
	return false
}

func validity(t evidencegraph.TemporalRecord, now time.Time) (bool, string) {
	var start, end time.Time
	var err error
	if t.ValidFrom != "" {
		start, err = time.Parse(time.RFC3339Nano, t.ValidFrom)
		if err != nil {
			return false, "invalid_validity_window"
		}
	}
	if t.ValidTo != "" {
		end, err = time.Parse(time.RFC3339Nano, t.ValidTo)
		if err != nil {
			return false, "invalid_validity_window"
		}
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return false, "invalid_validity_window"
	}
	if !start.IsZero() && now.Before(start) {
		return true, "not_yet_valid"
	}
	if !end.IsZero() && !now.Before(end) {
		return true, "expired"
	}
	return true, ""
}
