package evidenceingestion

const (
	// EvidenceQueryModeExactLexical runs only the existing simple all-term lookup.
	EvidenceQueryModeExactLexical = "exact_lexical"
	// EvidenceQueryModeDeterministicLexicalRecovery runs the bounded versioned recovery plan.
	EvidenceQueryModeDeterministicLexicalRecovery = "deterministic_lexical_recovery"
	// EvidenceQueryModeExperimentalHanRecoveryV1 adds an opt-in, zero-result Han fallback.
	EvidenceQueryModeExperimentalHanRecoveryV1 = "experimental_han_lexical_recovery_v1"
	// EvidenceQueryModeExperimentalMultisurfaceV1 searches statements and bounded identity source views.
	EvidenceQueryModeExperimentalMultisurfaceV1 = "experimental_multisurface_lexical_v1"
	// EvidenceQueryModePracticalMultisurfaceV1 permits one empty-result English recovery pass.
	EvidenceQueryModePracticalMultisurfaceV1 = "practical_multisurface_lexical_v1"

	// EvidenceQueryPlanExactV1 identifies the existing exact lexical plan.
	EvidenceQueryPlanExactV1 = "exact-lexical-v1"
	// EvidenceQueryPlanRecoveryV1 identifies the legacy first-non-empty recovery plan.
	EvidenceQueryPlanRecoveryV1 = "lexical-recovery-v1"
	// EvidenceQueryPlanRecoveryV2 identifies morphology-stable deterministic lexical recovery.
	EvidenceQueryPlanRecoveryV2 = "lexical-recovery-v2"
	// EvidenceQueryPlanExperimentalHanRecoveryV1 preserves recovery-v2 before a literal Han fallback.
	EvidenceQueryPlanExperimentalHanRecoveryV1 = "experimental-han-lexical-recovery-v1"
	// EvidenceQueryPlanExperimentalMultisurfaceV1 unions baseline and lexical surface candidates.
	EvidenceQueryPlanExperimentalMultisurfaceV1 = "experimental-multisurface-lexical-v1"
	// EvidenceQueryPlanPracticalMultisurfaceV2 separates Han anchor eligibility from match scoring.
	EvidenceQueryPlanPracticalMultisurfaceV2 = "practical-multisurface-lexical-v2"
	// EvidencePracticalHanTermPolicyVersionV1 identifies the fixed auxiliary-bigram policy.
	EvidencePracticalHanTermPolicyVersionV1 = "han-auxiliary-anchor-v1"
	// EvidenceQueryNormalizerSimpleV1 identifies PostgreSQL simple FTS normalization.
	EvidenceQueryNormalizerSimpleV1 = "postgresql-simple-v1"
	// EvidenceQueryNormalizerRecoveryV1 identifies the PostgreSQL simple/English FTS normalization pair.
	EvidenceQueryNormalizerRecoveryV1 = "postgresql-simple-english-v1"
	// EvidenceQueryNormalizerExperimentalHanV1 adds literal Han tokens to the existing FTS pair.
	EvidenceQueryNormalizerExperimentalHanV1 = "postgresql-simple-english-han-literal-v1"
	// EvidenceQueryNormalizerExperimentalMultisurfaceV1 identifies dictionary-free Han bigrams and English lexemes.
	EvidenceQueryNormalizerExperimentalMultisurfaceV1 = "unicode-han-bigram-postgresql-english-v1"
	// EvidenceQuerySearchSurfaceProposalStatement is the sole legacy/default text search surface.
	EvidenceQuerySearchSurfaceProposalStatement = "proposal_occurrences.statement_text"
	// EvidenceQuerySearchSurfaceIdentitySource is the full bounded manual identity view, not proposal refs.
	EvidenceQuerySearchSurfaceIdentitySource = "extraction_views.rendered_content"

	// EvidenceQueryStrategyExactSimple requires every simple-config lexeme.
	EvidenceQueryStrategyExactSimple = "exact_simple_all_terms"
	// EvidenceQueryStrategyEnglishMorphology requires every English stemmed lexeme.
	EvidenceQueryStrategyEnglishMorphology = "english_morphology_all_terms"
	// EvidenceQueryStrategyEnglishRelaxation requires at least two English stemmed lexemes.
	EvidenceQueryStrategyEnglishRelaxation = "english_lexeme_overlap_min_2"
	// EvidenceQueryStrategyHanLiteralAllTerms requires every Han literal and every simple ASCII lexeme.
	EvidenceQueryStrategyHanLiteralAllTerms = "han_literal_and_ascii_simple_all_terms"
	// EvidenceQueryStrategyMultisurface unions baseline IDs and independently matched lexical surfaces.
	EvidenceQueryStrategyMultisurface = "baseline_union_han_bigram_or_english_overlap"
	// EvidenceQueryStrategyPracticalRecovery is one additional single-English-term lookup.
	EvidenceQueryStrategyPracticalRecovery = "practical_single_english_term_recovery"

	// EvidenceQueryCompletionExactCandidates indicates that the exact strategy returned candidates.
	EvidenceQueryCompletionExactCandidates = "exact_candidates_found"
	// EvidenceQueryCompletionMorphologyCandidates indicates that morphology recovery returned candidates.
	EvidenceQueryCompletionMorphologyCandidates = "morphology_candidates_found"
	// EvidenceQueryCompletionRelatedCandidates indicates that bounded lexical relaxation returned candidates.
	EvidenceQueryCompletionRelatedCandidates = "related_candidates_found"
	// EvidenceQueryCompletionHanCandidates indicates lexical candidates, not semantic support.
	EvidenceQueryCompletionHanCandidates = "han_literal_candidates_found"
	// EvidenceQueryCompletionMultisurfaceCandidates describes retrieval, never source support.
	EvidenceQueryCompletionMultisurfaceCandidates = "multisurface_lexical_candidates_found"
	// EvidenceQueryCompletionPracticalFirstCandidates identifies practical first-pass retrieval.
	EvidenceQueryCompletionPracticalFirstCandidates = "practical_multisurface_lexical_candidates_found"
	// EvidenceQueryCompletionPracticalCandidates records retrieval, not source support.
	EvidenceQueryCompletionPracticalCandidates = "practical_single_term_recovery_candidates_found"
	// EvidenceQueryCompletionPracticalNoMatch is bounded, never a global absence claim.
	EvidenceQueryCompletionPracticalNoMatch = "practical_single_term_recovery_no_match"
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
	// FallbackStatus belongs to Han recovery; RankingPolicy also describes multisurface plans.
	FallbackStatus    string
	RankingPolicy     string
	Multisurface      *EvidenceMultisurfaceExecution
	PracticalRecovery *EvidencePracticalRecovery
}

// EvidencePracticalRecovery reports the configured threshold, before min(threshold, term count).
type EvidencePracticalRecovery struct {
	FallbackAttempted             bool
	ConfiguredMinimumEnglishTerms int
	HanTermPolicy                 EvidencePracticalHanTermPolicy
}

// EvidencePracticalHanTermPolicy reports query-local eligibility, not segmentation or topic detection.
type EvidencePracticalHanTermPolicy struct {
	Version                     string
	AuxiliaryTerms              []string
	EligibleAnchorTerms         []string
	AuxiliaryOnlyQueryPreserved bool
}

// EvidenceMultisurfaceExecution separates the ID-only recovery-v2 baseline from
// the supplemental search. Exclusions and a truncated baseline preclude a
// completeness claim even when the final union page fits within its limit.
type EvidenceMultisurfaceExecution struct {
	Baseline             EvidenceQueryExecution
	NormalizedQueryTerms []string
	SearchedSourceViews  int
	ExcludedSourceViews  int
}

// EvidenceRetrievalBasis explains candidate selection, not evidence support.
type EvidenceRetrievalBasis struct {
	NormalizedQueryTerms []string
	BaselineMatched      bool
	Surfaces             []EvidenceRetrievalSurface
}

// EvidenceRetrievalSurface retains exact terms and source identity separately
// from the proposal's original, immutable source references.
type EvidenceRetrievalSurface struct {
	Surface             string
	Status              string
	MatchedTerms        []string
	MissingTerms        []string
	SourceSnapshotID    string
	ExtractionViewID    string
	RawContentHash      string
	RenderedContentHash string
	MatchedSpans        []EvidenceRetrievalSpan
	SpansTruncated      bool
}

// EvidenceRetrievalSpan is one exact source span containing lexical matches.
// WithinProposalSourceRefs requires the same view and containment of its range.
type EvidenceRetrievalSpan struct {
	SourceRef                ResolvedSourceRef
	MatchedTerms             []string
	WithinProposalSourceRefs bool
}

// ProposalSearchQueryResult contains an exact lexical result page and its execution boundary.
type ProposalSearchQueryResult struct {
	Matches   []ProposalSearchResult
	Execution EvidenceQueryExecution
}
