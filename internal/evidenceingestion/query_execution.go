package evidenceingestion

const (
	// EvidenceQueryModeExactLexical runs only the existing simple all-term lookup.
	EvidenceQueryModeExactLexical = "exact_lexical"
	// EvidenceQueryModeDeterministicLexicalRecovery runs the bounded versioned recovery plan.
	EvidenceQueryModeDeterministicLexicalRecovery = "deterministic_lexical_recovery"

	// EvidenceQueryPlanExactV1 identifies the existing exact lexical plan.
	EvidenceQueryPlanExactV1 = "exact-lexical-v1"
	// EvidenceQueryPlanRecoveryV1 identifies the legacy first-non-empty recovery plan.
	EvidenceQueryPlanRecoveryV1 = "lexical-recovery-v1"
	// EvidenceQueryPlanRecoveryV2 identifies morphology-stable deterministic lexical recovery.
	EvidenceQueryPlanRecoveryV2 = "lexical-recovery-v2"
	// EvidenceQueryNormalizerSimpleV1 identifies PostgreSQL simple FTS normalization.
	EvidenceQueryNormalizerSimpleV1 = "postgresql-simple-v1"
	// EvidenceQueryNormalizerRecoveryV1 identifies the PostgreSQL simple/English FTS normalization pair.
	EvidenceQueryNormalizerRecoveryV1 = "postgresql-simple-english-v1"
	// EvidenceQuerySearchSurfaceProposalStatement is the only text searched by the current query core.
	EvidenceQuerySearchSurfaceProposalStatement = "proposal_occurrences.statement_text"

	// EvidenceQueryStrategyExactSimple requires every simple-config lexeme.
	EvidenceQueryStrategyExactSimple = "exact_simple_all_terms"
	// EvidenceQueryStrategyEnglishMorphology requires every English stemmed lexeme.
	EvidenceQueryStrategyEnglishMorphology = "english_morphology_all_terms"
	// EvidenceQueryStrategyEnglishRelaxation requires at least two English stemmed lexemes.
	EvidenceQueryStrategyEnglishRelaxation = "english_lexeme_overlap_min_2"

	// EvidenceQueryCompletionExactCandidates indicates that the exact strategy returned candidates.
	EvidenceQueryCompletionExactCandidates = "exact_candidates_found"
	// EvidenceQueryCompletionMorphologyCandidates indicates that morphology recovery returned candidates.
	EvidenceQueryCompletionMorphologyCandidates = "morphology_candidates_found"
	// EvidenceQueryCompletionRelatedCandidates indicates that bounded lexical relaxation returned candidates.
	EvidenceQueryCompletionRelatedCandidates = "related_candidates_found"
	// EvidenceQueryCompletionBoundedNoMatch indicates that every planned strategy returned zero candidates.
	EvidenceQueryCompletionBoundedNoMatch = "bounded_retrieval_no_match"
)

// EvidenceQueryFilters records the exact filters applied by Query Core.
type EvidenceQueryFilters struct {
	SourceSnapshotID     string
	RepositorySnapshotID string
	SourceGenerationID   string
	SourceID             string
	SourceVersion        string
	AdmissionOutcome     string
	LifecycleScope       string
}

// EvidenceQueryAttempt records one deterministic lexical lookup.
type EvidenceQueryAttempt struct {
	Strategy                string
	TextSearchConfiguration string
	CompiledQuery           string
	NormalizedQueryTerms    []string
	CandidateCount          int
	Truncated               bool
}

// EvidenceQueryExecution is the authoritative description of one bounded query plan.
type EvidenceQueryExecution struct {
	OriginalQuery                 string
	QueryMode                     string
	PlanVersion                   string
	NormalizerVersion             string
	SearchSurface                 string
	SearchedFields                []string
	SearchedRecordKinds           []string
	EligibleSourceBindingKinds    []string
	Filters                       EvidenceQueryFilters
	Limit                         int
	QueryCount                    int
	CandidateCount                int
	Truncated                     bool
	SearchCompleteWithinSurface   bool
	CompletionReason              string
	GlobalAbsenceInferenceAllowed bool
	Attempts                      []EvidenceQueryAttempt
}

// ProposalSearchQueryResult contains an exact lexical result page and its execution boundary.
type ProposalSearchQueryResult struct {
	Matches   []ProposalSearchResult
	Execution EvidenceQueryExecution
}
