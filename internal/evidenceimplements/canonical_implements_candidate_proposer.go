package evidenceimplements

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

const (
	// CandidateContractVersion preserves the retained review contract value.
	CandidateContractVersion = "lab-canonical-implements-candidate-proposer/v1"
	// CandidatePolicyEffect preserves the retained review contract value.
	CandidatePolicyEffect = "review_candidate_only_no_canonical_mutation"
	// CandidateDirection preserves the retained review contract value.
	CandidateDirection = "specification_claim_to_implementation_claim"

	// QualifiedSymbolMethod preserves the retained review contract value.

	QualifiedSymbolMethod CandidateMethod = "exact_qualified_symbol_anchor"
	// UniqueSimpleMethod preserves the retained review contract value.
	UniqueSimpleMethod CandidateMethod = "unique_simple_symbol_anchor"
	// LexicalMethod preserves the retained review contract value.
	LexicalMethod CandidateMethod = "bounded_lexical_overlap"

	// QualifiedSymbolPath preserves the retained review contract value.

	QualifiedSymbolPath = "specification_mentions_qualified_symbol_to_implementation"
	// UniqueSimplePath preserves the retained review contract value.
	UniqueSimplePath = "specification_mentions_cut_unique_symbol_to_implementation"
	// LexicalPath preserves the retained review contract value.
	LexicalPath = "specification_and_implementation_share_lexical_terms"

	// NoEvidenceGap preserves the retained review contract value.

	NoEvidenceGap CandidateGapKind = "no_bounded_evidence_path"
	// AmbiguousSymbolGap preserves the retained review contract value.
	AmbiguousSymbolGap CandidateGapKind = "ambiguous_simple_symbol_anchor"
	// AmbiguousQNameGap preserves the retained review contract value.
	AmbiguousQNameGap CandidateGapKind = "ambiguous_qualified_symbol_anchor"

	// MaxCandidatesPerSpecification preserves the retained review contract value.

	MaxCandidatesPerSpecification = 8
	// MaxCandidatesPerReport preserves the retained review contract value.
	MaxCandidatesPerReport = 64
	// MaxCandidatePairEvaluations preserves the retained review contract value.
	MaxCandidatePairEvaluations = 4096
	// MaxCandidateSharedTerms preserves the retained review contract value.
	MaxCandidateSharedTerms = 32
	// MaxCandidateGapRecords preserves the retained review contract value.
	MaxCandidateGapRecords = 256
	// MaxCandidateOmittedIDs preserves the retained review contract value.
	MaxCandidateOmittedIDs = 32
	// MaxCandidateReportBytes preserves the retained review contract value.
	MaxCandidateReportBytes = 4 * 1024 * 1024
	// MinSharedLexicalTerms preserves the retained review contract value.
	MinSharedLexicalTerms = 2

	// ReviewTemplate preserves the retained review contract value.

	ReviewTemplate = "review_whether_implementation_realizes_specification/v1"
)

var canonicalImplementsQualifiedIdentifierPattern = regexp.MustCompile(
	`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+`,
)

var canonicalImplementsSimpleIdentifierPattern = regexp.MustCompile(
	`[A-Za-z_][A-Za-z0-9_]*`,
)

var canonicalImplementsLexicalStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {},
	"be": {}, "before": {}, "by": {}, "can": {}, "do": {}, "does": {},
	"for": {}, "from": {}, "if": {}, "in": {}, "into": {}, "is": {},
	"it": {}, "must": {}, "not": {}, "of": {}, "on": {}, "or": {},
	"return": {}, "returns": {}, "shall": {}, "should": {}, "that": {},
	"the": {}, "then": {}, "this": {}, "to": {}, "when": {}, "with": {},

	// Relation labels cannot bootstrap their own candidate score.
	"implement": {}, "implementation": {}, "implements": {}, "implemented": {},
}

// CandidateMethod is a versioned pure review value; constructing it does not grant canonical write authority.
type CandidateMethod string

// CandidateGapKind is a versioned pure review value; constructing it does not grant canonical write authority.
type CandidateGapKind string

// CandidateEvidencePath is a versioned pure review value; constructing it does not grant canonical write authority.
type CandidateEvidencePath struct {
	TemplateID           string   `json:"template_id"`
	SpecificationNodeID  string   `json:"specification_node_id"`
	AnchorKind           string   `json:"anchor_kind"`
	AnchorValues         []string `json:"anchor_values"`
	ImplementationNodeID string   `json:"implementation_node_id"`
}

// Candidate is a versioned pure review value; constructing it does not grant canonical write authority.
type Candidate struct {
	ContractVersion         string                              `json:"contract_version"`
	ID                      string                              `json:"id"`
	AdmittedCutID           string                              `json:"admitted_cut_id"`
	ProposedRelation        evidencegraph.CanonicalEdgeRelation `json:"proposed_relation"`
	Direction               string                              `json:"direction"`
	Method                  CandidateMethod                     `json:"method"`
	RankWithinSpecification int                                 `json:"rank_within_specification"`
	Specification           Specification                       `json:"specification"`
	Implementation          Implementation                      `json:"implementation"`
	EvidencePath            CandidateEvidencePath               `json:"evidence_path"`
	SharedLexicalTerms      []string                            `json:"shared_lexical_terms"`
	SharedLexicalTermCount  int                                 `json:"shared_lexical_term_count"`
	OmittedSharedTermCount  int                                 `json:"omitted_shared_lexical_term_count"`
	ReviewTemplateID        string                              `json:"review_template_id"`
	Coverage                string                              `json:"coverage"`
	Limitations             []string                            `json:"limitations"`
	PolicyEffect            string                              `json:"policy_effect"`
}

// CandidateGap is a versioned pure review value; constructing it does not grant canonical write authority.
type CandidateGap struct {
	Kind                  CandidateGapKind `json:"kind"`
	SpecificationNodeID   string           `json:"specification_node_id"`
	Anchor                string           `json:"anchor,omitempty"`
	ImplementationNodeIDs []string         `json:"implementation_node_ids"`
}

// CandidateTruncation is a versioned pure review value; constructing it does not grant canonical write authority.
type CandidateTruncation struct {
	SpecificationNodeID    string   `json:"specification_node_id"`
	OmittedCandidateCount  int      `json:"omitted_candidate_count"`
	OmittedCandidateIDs    []string `json:"omitted_candidate_ids"`
	UnlistedOmittedIDCount int      `json:"unlisted_omitted_candidate_id_count"`
}

// CandidateReport is a versioned pure review value; constructing it does not grant canonical write authority.
type CandidateReport struct {
	ContractVersion                string                              `json:"contract_version"`
	ID                             string                              `json:"id"`
	AdmittedCutID                  string                              `json:"admitted_cut_id"`
	ProposedRelation               evidencegraph.CanonicalEdgeRelation `json:"proposed_relation"`
	Direction                      string                              `json:"direction"`
	PolicyEffect                   string                              `json:"policy_effect"`
	CandidateLimitPerSpecification int                                 `json:"candidate_limit_per_specification"`
	CandidateLimitPerReport        int                                 `json:"candidate_limit_per_report"`
	PairEvaluationLimit            int                                 `json:"pair_evaluation_limit"`
	EvaluatedPairCount             int                                 `json:"evaluated_pair_count"`
	GapRecordLimit                 int                                 `json:"gap_record_limit"`
	OmittedGapCount                int                                 `json:"omitted_gap_count"`
	Candidates                     []Candidate                         `json:"candidates"`
	Gaps                           []CandidateGap                      `json:"gaps"`
	Truncations                    []CandidateTruncation               `json:"truncations"`
}

// ProposeCandidates evaluates only the endpoint material in
// one validated admitted cut. Its input shape deliberately has no relation
// edges, pending proposals, model scores, reviewer fields, or caller-selected
// endpoint pair. The result is review material and has no canonical mutation
// effect.
// ProposeCandidates applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ProposeCandidates(
	cut AdmittedCut,
) (CandidateReport, error) {
	if err := ValidateAdmittedCut(cut); err != nil {
		return CandidateReport{}, err
	}
	pairCount := len(cut.Specifications) * len(cut.Implementations)
	if pairCount > MaxCandidatePairEvaluations {
		return CandidateReport{}, fmt.Errorf(
			"%w: proposer pair count %d exceeds %d",
			ErrInvalid,
			pairCount,
			MaxCandidatePairEvaluations,
		)
	}

	report := CandidateReport{
		ContractVersion:                CandidateContractVersion,
		AdmittedCutID:                  cut.ID,
		ProposedRelation:               evidencegraph.CanonicalImplements,
		Direction:                      CandidateDirection,
		PolicyEffect:                   CandidatePolicyEffect,
		CandidateLimitPerSpecification: MaxCandidatesPerSpecification,
		CandidateLimitPerReport:        MaxCandidatesPerReport,
		PairEvaluationLimit:            MaxCandidatePairEvaluations,
		EvaluatedPairCount:             pairCount,
		GapRecordLimit:                 MaxCandidateGapRecords,
		Candidates:                     make([]Candidate, 0),
		Gaps:                           make([]CandidateGap, 0),
		Truncations:                    make([]CandidateTruncation, 0),
	}

	qualifiedOwners := canonicalImplementsQualifiedSymbolOwners(cut.Implementations)
	simpleOwners := canonicalImplementsSimpleSymbolOwners(cut.Implementations)
	globalCandidates := make([]Candidate, 0)
	omittedBySpecification := make(map[string][]string)
	for _, specification := range cut.Specifications {
		identifiers := canonicalImplementsStandaloneIdentifierSet(specification.ClaimText)
		for _, gap := range canonicalImplementsAmbiguousQualifiedSymbolGaps(
			specification.NodeID,
			specification.ClaimText,
			qualifiedOwners,
		) {
			canonicalImplementsAppendCandidateGap(&report, gap)
		}
		for _, gap := range canonicalImplementsAmbiguousSymbolGaps(specification.NodeID, identifiers, simpleOwners) {
			canonicalImplementsAppendCandidateGap(&report, gap)
		}

		candidates := make([]Candidate, 0, len(cut.Implementations))
		for _, implementation := range cut.Implementations {
			candidate, ok, err := newCanonicalImplementsCandidate(
				cut.ID,
				specification,
				implementation,
				identifiers,
				simpleOwners,
			)
			if err != nil {
				return CandidateReport{}, err
			}
			if ok {
				candidates = append(candidates, candidate)
			}
		}
		canonicalImplementsSortCandidates(candidates)
		if len(candidates) == 0 {
			canonicalImplementsAppendCandidateGap(&report, CandidateGap{
				Kind:                  NoEvidenceGap,
				SpecificationNodeID:   specification.NodeID,
				ImplementationNodeIDs: []string{},
			})
			continue
		}

		for index := range candidates {
			candidates[index].RankWithinSpecification = index + 1
		}
		perSpecificationCount := min(len(candidates), MaxCandidatesPerSpecification)
		globalCandidates = append(globalCandidates, candidates[:perSpecificationCount]...)
		for _, candidate := range candidates[perSpecificationCount:] {
			omittedBySpecification[specification.NodeID] = append(
				omittedBySpecification[specification.NodeID],
				candidate.ID,
			)
		}
	}

	canonicalImplementsSortGlobalCandidates(globalCandidates)
	emitCount := min(len(globalCandidates), MaxCandidatesPerReport)
	for _, candidate := range globalCandidates[emitCount:] {
		omittedBySpecification[candidate.Specification.NodeID] = append(
			omittedBySpecification[candidate.Specification.NodeID],
			candidate.ID,
		)
	}
	report.Candidates = append(report.Candidates, globalCandidates[:emitCount]...)
	truncatedSpecificationIDs := make([]string, 0, len(omittedBySpecification))
	for specificationNodeID := range omittedBySpecification {
		truncatedSpecificationIDs = append(truncatedSpecificationIDs, specificationNodeID)
	}
	slices.Sort(truncatedSpecificationIDs)
	for _, specificationNodeID := range truncatedSpecificationIDs {
		omittedIDs := omittedBySpecification[specificationNodeID]
		slices.Sort(omittedIDs)
		listedCount := min(len(omittedIDs), MaxCandidateOmittedIDs)
		report.Truncations = append(report.Truncations, CandidateTruncation{
			SpecificationNodeID:    specificationNodeID,
			OmittedCandidateCount:  len(omittedIDs),
			OmittedCandidateIDs:    append([]string{}, omittedIDs[:listedCount]...),
			UnlistedOmittedIDCount: len(omittedIDs) - listedCount,
		})
	}

	slices.SortFunc(report.Gaps, func(left, right CandidateGap) int {
		if order := strings.Compare(left.SpecificationNodeID, right.SpecificationNodeID); order != 0 {
			return order
		}
		if order := strings.Compare(string(left.Kind), string(right.Kind)); order != 0 {
			return order
		}
		return strings.Compare(left.Anchor, right.Anchor)
	})

	reportID, err := CandidateReportID(report)
	if err != nil {
		return CandidateReport{}, err
	}
	report.ID = reportID
	encoded, err := json.Marshal(report)
	if err != nil {
		return CandidateReport{}, fmt.Errorf("encoding candidate report size: %w", err)
	}
	if len(encoded) > MaxCandidateReportBytes {
		return CandidateReport{}, fmt.Errorf(
			"%w: proposer report size %d exceeds %d bytes",
			ErrInvalid,
			len(encoded),
			MaxCandidateReportBytes,
		)
	}
	return report, nil
}

// ValidateCandidateReport applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateCandidateReport(
	cut AdmittedCut,
	report CandidateReport,
) error {
	want, err := ProposeCandidates(cut)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(report, want) {
		return fmt.Errorf("%w: implements candidate report is not canonical", ErrInvalid)
	}
	return nil
}

func newCanonicalImplementsCandidate(
	cutID string,
	specification Specification,
	implementation Implementation,
	identifiers map[string]struct{},
	simpleOwners map[string][]string,
) (Candidate, bool, error) {
	qualified := implementation.QualifiedName
	simple := canonicalImplementsSimpleSymbol(qualified)
	allSharedTerms := canonicalImplementsSharedLexicalTerms(specification, implementation)
	sharedTerms := allSharedTerms[:min(len(allSharedTerms), MaxCandidateSharedTerms)]

	method := CandidateMethod("")
	pathTemplate := ""
	anchorKind := ""
	anchors := make([]string, 0)
	switch {
	case canonicalImplementsContainsQualifiedIdentifier(specification.ClaimText, qualified):
		method = QualifiedSymbolMethod
		pathTemplate = QualifiedSymbolPath
		anchorKind = "qualified_symbol"
		anchors = append(anchors, qualified)
	case canonicalImplementsIdentifierExists(identifiers, simple) && len(simpleOwners[simple]) == 1:
		method = UniqueSimpleMethod
		pathTemplate = UniqueSimplePath
		anchorKind = "simple_symbol"
		anchors = append(anchors, simple)
	case len(allSharedTerms) >= MinSharedLexicalTerms:
		method = LexicalMethod
		pathTemplate = LexicalPath
		anchorKind = "lexical_term"
		anchors = append(anchors, sharedTerms...)
	default:
		return Candidate{}, false, nil
	}

	candidate := Candidate{
		ContractVersion:  CandidateContractVersion,
		AdmittedCutID:    cutID,
		ProposedRelation: evidencegraph.CanonicalImplements,
		Direction:        CandidateDirection,
		Method:           method,
		Specification:    specification,
		Implementation:   implementation,
		EvidencePath: CandidateEvidencePath{
			TemplateID:           pathTemplate,
			SpecificationNodeID:  specification.NodeID,
			AnchorKind:           anchorKind,
			AnchorValues:         anchors,
			ImplementationNodeID: implementation.NodeID,
		},
		SharedLexicalTerms:     append([]string{}, sharedTerms...),
		SharedLexicalTermCount: len(allSharedTerms),
		OmittedSharedTermCount: len(allSharedTerms) - len(sharedTerms),
		ReviewTemplateID:       ReviewTemplate,
		Coverage: fmt.Sprintf(
			"Candidate generated from one canonical specification claim and one pinned implementation symbol in admitted cut %s.",
			cutID,
		),
		Limitations:  canonicalImplementsCandidateLimitations(method),
		PolicyEffect: CandidatePolicyEffect,
	}
	candidateID, err := CandidateID(candidate)
	if err != nil {
		return Candidate{}, false, err
	}
	candidate.ID = candidateID
	return candidate, true, nil
}

func canonicalImplementsCandidateLimitations(method CandidateMethod) []string {
	limitations := []string{
		"This is unreviewed candidate material, not an admitted canonical edge.",
		"The evidence path does not prove behavioral completeness, correctness, deployment, or currentness.",
		"Coverage is limited to the immutable endpoint material in the named admitted cut.",
		"This pure proposer cannot prove that an upstream cut loader selected endpoints without existing relation, candidate, or review data.",
	}
	switch method {
	case QualifiedSymbolMethod:
		limitations = append(limitations, "A qualified-symbol mention may be a reference, negation, or example rather than an implementation mapping.")
	case UniqueSimpleMethod:
		limitations = append(limitations, "Simple-symbol uniqueness applies only inside this admitted cut and does not establish the source author's intent.")
	case LexicalMethod:
		limitations = append(limitations, "Lexical overlap is vocabulary-dependent and may rank coincidental terminology or miss semantic equivalence.")
	}
	return limitations
}

func canonicalImplementsQualifiedSymbolOwners(
	implementations []Implementation,
) map[string][]string {
	owners := make(map[string][]string)
	for _, implementation := range implementations {
		if !canonicalImplementsSupportedQualifiedName(implementation.QualifiedName) {
			continue
		}
		owners[implementation.QualifiedName] = append(
			owners[implementation.QualifiedName],
			implementation.NodeID,
		)
	}
	for qualifiedName := range owners {
		slices.Sort(owners[qualifiedName])
	}
	return owners
}

func canonicalImplementsAmbiguousQualifiedSymbolGaps(
	specificationNodeID string,
	specificationClaim string,
	qualifiedOwners map[string][]string,
) []CandidateGap {
	gaps := make([]CandidateGap, 0, len(qualifiedOwners))
	for anchor, implementationNodeIDs := range qualifiedOwners {
		if len(implementationNodeIDs) < 2 || !canonicalImplementsContainsQualifiedIdentifier(specificationClaim, anchor) {
			continue
		}
		gaps = append(gaps, CandidateGap{
			Kind:                  AmbiguousQNameGap,
			SpecificationNodeID:   specificationNodeID,
			Anchor:                anchor,
			ImplementationNodeIDs: append([]string{}, implementationNodeIDs...),
		})
	}
	slices.SortFunc(gaps, func(left, right CandidateGap) int {
		return strings.Compare(left.Anchor, right.Anchor)
	})
	return gaps
}

func canonicalImplementsAppendCandidateGap(
	report *CandidateReport,
	gap CandidateGap,
) {
	if len(report.Gaps) == MaxCandidateGapRecords {
		report.OmittedGapCount++
		return
	}
	report.Gaps = append(report.Gaps, gap)
}

func canonicalImplementsSimpleSymbolOwners(
	implementations []Implementation,
) map[string][]string {
	owners := make(map[string][]string)
	for _, implementation := range implementations {
		if !canonicalImplementsSupportedQualifiedName(implementation.QualifiedName) {
			continue
		}
		symbol := canonicalImplementsSimpleSymbol(implementation.QualifiedName)
		if symbol == "" {
			continue
		}
		owners[symbol] = append(owners[symbol], implementation.NodeID)
	}
	for symbol := range owners {
		slices.Sort(owners[symbol])
	}
	return owners
}

func canonicalImplementsAmbiguousSymbolGaps(
	specificationNodeID string,
	identifiers map[string]struct{},
	simpleOwners map[string][]string,
) []CandidateGap {
	gaps := make([]CandidateGap, 0, len(simpleOwners))
	for anchor, implementationNodeIDs := range simpleOwners {
		if len(implementationNodeIDs) < 2 || !canonicalImplementsIdentifierExists(identifiers, anchor) {
			continue
		}
		gaps = append(gaps, CandidateGap{
			Kind:                  AmbiguousSymbolGap,
			SpecificationNodeID:   specificationNodeID,
			Anchor:                anchor,
			ImplementationNodeIDs: append([]string{}, implementationNodeIDs...),
		})
	}
	slices.SortFunc(gaps, func(left, right CandidateGap) int {
		return strings.Compare(left.Anchor, right.Anchor)
	})
	return gaps
}

func canonicalImplementsStandaloneIdentifierSet(text string) map[string]struct{} {
	identifiers := make(map[string]struct{})
	qualifiedSpans := canonicalImplementsQualifiedIdentifierPattern.FindAllStringIndex(text, -1)
	for _, span := range canonicalImplementsSimpleIdentifierPattern.FindAllStringIndex(text, -1) {
		if !canonicalImplementsIdentifierBoundaries(text, span[0], span[1]) || canonicalImplementsSpanContained(span, qualifiedSpans) {
			continue
		}
		identifiers[text[span[0]:span[1]]] = struct{}{}
	}
	return identifiers
}

func canonicalImplementsIdentifierExists(identifiers map[string]struct{}, target string) bool {
	if target == "" {
		return false
	}
	_, exists := identifiers[target]
	return exists
}

func canonicalImplementsContainsQualifiedIdentifier(text, target string) bool {
	if !canonicalImplementsSupportedQualifiedName(target) {
		return false
	}
	for _, span := range canonicalImplementsQualifiedIdentifierPattern.FindAllStringIndex(text, -1) {
		if canonicalImplementsIdentifierBoundaries(text, span[0], span[1]) && text[span[0]:span[1]] == target {
			return true
		}
	}
	return false
}

func canonicalImplementsSupportedQualifiedName(value string) bool {
	spans := canonicalImplementsQualifiedIdentifierPattern.FindAllStringIndex(value, -1)
	return len(spans) == 1 && spans[0][0] == 0 && spans[0][1] == len(value)
}

func canonicalImplementsIdentifierBoundaries(text string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if canonicalImplementsIdentifierRune(previous) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if canonicalImplementsIdentifierRune(next) {
			return false
		}
	}
	return true
}

func canonicalImplementsIdentifierRune(value rune) bool {
	return value == '.' || value == '_' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func canonicalImplementsSpanContained(span []int, containers [][]int) bool {
	for _, container := range containers {
		if container[0] <= span[0] && span[1] <= container[1] {
			return true
		}
	}
	return false
}

func canonicalImplementsSimpleSymbol(qualifiedName string) string {
	identifiers := canonicalImplementsSimpleIdentifierPattern.FindAllString(qualifiedName, -1)
	if len(identifiers) == 0 {
		return ""
	}
	return identifiers[len(identifiers)-1]
}

func canonicalImplementsSharedLexicalTerms(
	specification Specification,
	implementation Implementation,
) []string {
	specificationTerms := LexicalTerms(specification.ClaimText)
	implementationTerms := LexicalTerms(strings.Join([]string{
		implementation.QualifiedName,
		implementation.Path,
		implementation.ExactExcerpt,
	}, " "))
	shared := make([]string, 0)
	for term := range specificationTerms {
		if _, exists := implementationTerms[term]; exists {
			shared = append(shared, term)
		}
	}
	slices.Sort(shared)
	return shared
}

// LexicalTerms retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func LexicalTerms(text string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, term := range canonicalImplementsSplitWords(text) {
		term = strings.ToLower(term)
		if !canonicalImplementsLexicalTermLongEnough(term) || !canonicalImplementsContainsLetter(term) {
			continue
		}
		if _, stopped := canonicalImplementsLexicalStopwords[term]; stopped {
			continue
		}
		terms[term] = struct{}{}
	}
	return terms
}

func canonicalImplementsLexicalTermLongEnough(value string) bool {
	runeCount := utf8.RuneCountInString(value)
	if runeCount < 2 {
		return false
	}
	for _, item := range value {
		if item > unicode.MaxASCII {
			return true
		}
	}
	return runeCount >= 3
}

func canonicalImplementsSplitWords(text string) []string {
	runes := []rune(text)
	words := make([]string, 0)
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			words = append(words, string(runes[start:end]))
		}
		start = -1
	}
	for index, current := range runes {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) {
			flush(index)
			continue
		}
		if start < 0 {
			start = index
			continue
		}
		previous := runes[index-1]
		var next rune
		if index+1 < len(runes) {
			next = runes[index+1]
		}
		if unicode.IsUpper(current) && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			flush(index)
			start = index
		} else if unicode.IsUpper(current) && unicode.IsUpper(previous) && next != 0 && unicode.IsLower(next) {
			flush(index)
			start = index
		}
	}
	flush(len(runes))
	return words
}

func canonicalImplementsContainsLetter(value string) bool {
	for _, item := range value {
		if unicode.IsLetter(item) {
			return true
		}
	}
	return false
}

func canonicalImplementsSortCandidates(candidates []Candidate) {
	slices.SortFunc(candidates, func(left, right Candidate) int {
		if order := canonicalImplementsCandidateMethodStrength(right.Method) - canonicalImplementsCandidateMethodStrength(left.Method); order != 0 {
			return order
		}
		if order := right.SharedLexicalTermCount - left.SharedLexicalTermCount; order != 0 {
			return order
		}
		if order := strings.Compare(left.Implementation.QualifiedName, right.Implementation.QualifiedName); order != 0 {
			return order
		}
		return strings.Compare(left.Implementation.NodeID, right.Implementation.NodeID)
	})
}

func canonicalImplementsSortGlobalCandidates(candidates []Candidate) {
	slices.SortFunc(candidates, func(left, right Candidate) int {
		if order := canonicalImplementsCandidateMethodStrength(right.Method) - canonicalImplementsCandidateMethodStrength(left.Method); order != 0 {
			return order
		}
		if order := right.SharedLexicalTermCount - left.SharedLexicalTermCount; order != 0 {
			return order
		}
		if order := strings.Compare(left.Specification.NodeID, right.Specification.NodeID); order != 0 {
			return order
		}
		if order := strings.Compare(left.Implementation.QualifiedName, right.Implementation.QualifiedName); order != 0 {
			return order
		}
		return strings.Compare(left.Implementation.NodeID, right.Implementation.NodeID)
	})
}

func canonicalImplementsCandidateMethodStrength(method CandidateMethod) int {
	switch method {
	case QualifiedSymbolMethod:
		return 3
	case UniqueSimpleMethod:
		return 2
	case LexicalMethod:
		return 1
	default:
		return 0
	}
}

// CandidateID retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func CandidateID(candidate Candidate) (string, error) {
	candidate.ID = ""
	candidate.RankWithinSpecification = 0
	return Digest("canonical-implements-candidate:v1", candidate)
}

// CandidateReportID retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func CandidateReportID(report CandidateReport) (string, error) {
	report.ID = ""
	return Digest("canonical-implements-candidate-report:v1", report)
}
