package evidenceimplements

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func validateRecursiveClosure(cut AdmittedCut, b RecursiveReviewBasis) error {
	a := b.Ancestors
	if err := a.Validate(); err != nil {
		return fmt.Errorf("%w: native recursive ancestor cut: %v", ErrInvalid, err)
	}
	nodes := make(map[string]evidencegraph.CanonicalNode, len(a.Nodes))
	derivations := make(map[string]evidencegraph.DerivationRecord, len(a.Derivations))
	payloads := make(map[string]evidencegraph.EvidencePayload, len(a.Payloads))
	provenance := make(map[string]evidencegraph.ProvenanceRecord, len(a.Provenance))
	for _, p := range a.Payloads {
		payloads[p.ID] = p
	}
	for _, p := range a.Provenance {
		provenance[p.ID] = p
	}
	for _, d := range a.Derivations {
		derivations[d.NodeID] = d
	}
	usedPayload, usedProvenance := make(map[string]bool, len(a.Payloads)), make(map[string]bool, len(a.Provenance))
	usedTemporal, usedIntegrity := make(map[string]bool, len(a.Temporal)), make(map[string]bool, len(a.Integrity))
	leaves := make(map[string]bool, len(b.SourceLeaves))
	for _, n := range a.Nodes {
		if n.Kind != evidencegraph.CanonicalDerivedClaim && n.Kind != evidencegraph.CanonicalSourceClaim {
			return fmt.Errorf("%w: recursive ancestors must be source or derived claims", ErrInvalid)
		}
		nodes[n.ID] = n
		spec, exists := FindSpecification(cut.Specifications, n.ID)
		p := payloads[n.PayloadRef]
		if !exists || spec.NodeKind != n.Kind || spec.ClaimText != p.Claim || spec.ClaimHash != ExcerptHash(p.Claim) {
			return fmt.Errorf("%w: native ancestor differs from admitted specification", ErrInvalid)
		}
		if n.Kind == evidencegraph.CanonicalDerivedClaim {
			d := derivations[n.ID]
			prov := provenance[d.ProvenanceRef]
			if spec.DerivationID != d.ID || spec.SourceTitle != p.Title || spec.SourceLocation != p.Source ||
				n.ProvenanceRef != d.ProvenanceRef || !recursiveSameSet(prov.OriginRefs, d.Parents) ||
				prov.Method != d.Method || prov.Producer != d.Producer || prov.TraceRef != d.TraceRef {
				return fmt.Errorf("%w: recursive derivation identity or AND provenance differs", ErrInvalid)
			}
		} else {
			leaves[n.ID] = true
		}
		usedPayload[n.PayloadRef], usedProvenance[n.ProvenanceRef] = true, true
		usedTemporal[n.TemporalRef], usedIntegrity[n.IntegrityRef] = true, true
	}
	if root, ok := nodes[b.RootNodeID]; !ok || root.Kind != evidencegraph.CanonicalDerivedClaim {
		return ErrEndpointMissing
	}
	seenEdges := make(map[[2]string]bool, len(a.Edges))
	for _, edge := range a.Edges {
		d, exists := derivations[edge.To]
		pair := [2]string{edge.From, edge.To}
		if !exists || edge.Relation != evidencegraph.CanonicalDerivedFrom || !slices.Contains(d.Parents, edge.From) || seenEdges[pair] {
			return fmt.Errorf("%w: recursive edge is not one exact declared AND parent", ErrInvalid)
		}
		p := provenance[edge.ProvenanceRef]
		if !slices.Equal(p.OriginRefs, []string{edge.From, edge.To}) || p.Method != d.Method || p.Producer != d.Producer || p.TraceRef != d.TraceRef {
			return fmt.Errorf("%w: recursive parent edge provenance differs", ErrInvalid)
		}
		seenEdges[pair], usedProvenance[edge.ProvenanceRef] = true, true
	}
	if len(usedPayload) != len(a.Payloads) || len(usedProvenance) != len(a.Provenance) ||
		len(usedTemporal) != len(a.Temporal) || len(usedIntegrity) != len(a.Integrity) {
		return fmt.Errorf("%w: unrelated recursive ancestor sidecar padding", ErrInvalid)
	}
	if err := recursiveClosureHeight(b.RootNodeID, nodes, derivations); err != nil {
		return err
	}
	seenRules := make(map[string]bool, len(b.Rules))
	for _, rule := range b.Rules {
		d, ok := derivations[rule.NodeID]
		if !ok || d.ID != rule.DerivationID || seenRules[rule.NodeID] {
			return fmt.Errorf("%w: rules do not bind the exact derivation set", ErrInvalid)
		}
		seenRules[rule.NodeID] = true
	}
	if len(seenRules) != len(derivations) {
		return fmt.Errorf("%w: a recursive derivation rule is missing", ErrInvalid)
	}
	seenLeaves := make(map[string]bool, len(b.SourceLeaves))
	for _, leaf := range b.SourceLeaves {
		if !leaves[leaf.SpecificationNodeID] || seenLeaves[leaf.SpecificationNodeID] {
			return fmt.Errorf("%w: recursive source reviews are not the exact leaf set", ErrInvalid)
		}
		if err := ValidateReviewEvidenceSnapshot(cut, leaf); err != nil {
			return err
		}
		seenLeaves[leaf.SpecificationNodeID] = true
	}
	if len(seenLeaves) != len(leaves) {
		return fmt.Errorf("%w: a recursive source review is missing", ErrInvalid)
	}
	return nil
}

func recursiveSameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, value := range a {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	for _, value := range b {
		if !seen[value] {
			return false
		}
	}
	return true
}

// Heights, rather than a visited-only depth test, catch the longest root path
// even when a shared ancestor was first encountered through a shorter branch.
// The active set separately detects cycles; memoization never blesses one.
func recursiveClosureHeight(root string, nodes map[string]evidencegraph.CanonicalNode, derivations map[string]evidencegraph.DerivationRecord) error {
	active := make(map[string]bool, MaxRecursiveDepth)
	heights := make(map[string]int, len(nodes))
	var visit func(string) (int, error)
	visit = func(id string) (int, error) {
		if active[id] {
			return 0, fmt.Errorf("%w: recursive AND evidence cycle", ErrInvalid)
		}
		if height, ok := heights[id]; ok {
			return height, nil
		}
		n, ok := nodes[id]
		if !ok {
			return 0, fmt.Errorf("%w: missing recursive AND parent", ErrInvalid)
		}
		if n.Kind == evidencegraph.CanonicalSourceClaim {
			heights[id] = 0
			return 0, nil
		}
		if len(active) >= MaxRecursiveDepth {
			return 0, fmt.Errorf("%w: recursive derived depth bound", ErrInvalid)
		}
		active[id] = true
		height := 1
		for _, parent := range derivations[id].Parents {
			parentHeight, err := visit(parent)
			if err != nil {
				return 0, err
			}
			height = max(height, 1+parentHeight)
		}
		delete(active, id)
		if height > MaxRecursiveDepth {
			return 0, fmt.Errorf("%w: recursive derived depth bound", ErrInvalid)
		}
		heights[id] = height
		return height, nil
	}
	if _, err := visit(root); err != nil {
		return err
	}
	if len(heights) != len(nodes) {
		return fmt.Errorf("%w: unrelated nodes outside recursive root closure", ErrInvalid)
	}
	return nil
}

// ValidateRecursiveBounds checks collection, string and aggregate byte bounds
// before graph indexing, source-display reconstruction or content-addressing.
func ValidateRecursiveBounds(b RecursiveReviewBasis) error {
	a := b.Ancestors
	if len(a.Nodes) < 2 || len(a.Nodes) > MaxRecursiveNodes || len(a.Edges) < 1 || len(a.Edges) > MaxRecursiveEdges ||
		len(a.Derivations) < 1 || len(a.Derivations) >= MaxRecursiveNodes || len(b.Rules) != len(a.Derivations) ||
		len(b.SourceLeaves) < 1 || len(b.SourceLeaves) >= MaxRecursiveNodes || len(a.Payloads) > MaxRecursiveNodes ||
		len(a.Provenance) > MaxRecursiveNodes+MaxRecursiveEdges || len(a.Temporal) > MaxRecursiveNodes || len(a.Integrity) > MaxRecursiveNodes {
		return fmt.Errorf("%w: recursive collection bound", ErrInvalid)
	}
	metadata := make([]string, 0, 6+6*len(a.Nodes)+6*len(a.Edges)+24*len(a.Payloads)+15*len(a.Provenance)+5*len(a.Temporal)+3*len(a.Integrity)+14*len(a.Derivations)+3*len(b.Rules))
	metadata = append(metadata, b.ContractVersion, b.ID, b.AdmittedCutID, b.RootNodeID, a.SchemaVersion, a.SnapshotID)
	for _, n := range a.Nodes {
		metadata = append(metadata, n.ID, string(n.Kind), n.PayloadRef, n.ProvenanceRef, n.TemporalRef, n.IntegrityRef)
	}
	for _, e := range a.Edges {
		metadata = append(metadata, e.ID, e.From, e.To, string(e.Relation), e.ProvenanceRef)
	}
	for _, p := range a.Payloads {
		if len(p.TargetAnchors) > MaxRecursiveParents {
			return ErrInvalid
		}
		metadata = append(metadata, p.ID, p.SourceType, p.Title, p.Source, p.Span, p.SpanLocator, p.Claim, p.Applicability)
		for _, target := range p.TargetAnchors {
			metadata = append(metadata, target.Kind, target.ID)
		}
	}
	for _, p := range a.Provenance {
		if len(p.OriginRefs) > MaxRecursiveParents {
			return ErrInvalid
		}
		metadata = append(metadata, p.ID, p.OriginGroupID, p.Producer, p.Method, p.MethodVersion, p.TraceRef, p.ReviewRef)
		metadata = append(metadata, p.OriginRefs...)
	}
	for _, v := range a.Temporal {
		metadata = append(metadata, v.ID, string(v.Status), v.ObservedAt, v.ValidFrom, v.ValidTo)
	}
	for _, v := range a.Integrity {
		metadata = append(metadata, v.ID, v.Algorithm, v.Digest)
	}
	for _, d := range a.Derivations {
		if len(d.Parents) < 1 || len(d.Parents) > MaxRecursiveParents {
			return ErrInvalid
		}
		metadata = append(metadata, d.ID, d.NodeID, d.Method, d.Producer, d.TraceRef, d.ProvenanceRef)
		metadata = append(metadata, d.Parents...)
	}
	for _, rule := range b.Rules {
		if rule.RuleStatement == "" || len(rule.RuleStatement) > MaxCoverageBytes || strings.TrimSpace(rule.RuleStatement) != rule.RuleStatement {
			return fmt.Errorf("%w: each derivation requires an explicit rule statement", ErrInvalid)
		}
		metadata = append(metadata, rule.NodeID, rule.DerivationID, rule.RuleStatement)
	}
	for _, value := range metadata {
		if len(value) > MaxExcerptBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return ErrInvalid
		}
	}
	for _, leaf := range b.SourceLeaves {
		if len(leaf.ID) > MaxLocationBytes || len(leaf.ContractVersion) > MaxLocationBytes {
			return ErrInvalid
		}
		if err := ValidateReviewEvidenceBounds(ReviewEvidenceInput{AdmittedCutID: leaf.AdmittedCutID,
			SpecificationNodeID: leaf.SpecificationNodeID, ReviewSnapshot: leaf.ReviewSnapshot, RawText: leaf.RawText, RenderedText: leaf.RenderedText}); err != nil {
			return err
		}
	}
	data, err := json.Marshal(b)
	if err != nil || len(data) > MaxDerivedBasisBytes {
		return fmt.Errorf("%w: recursive basis byte bound", ErrInvalid)
	}
	return nil
}
