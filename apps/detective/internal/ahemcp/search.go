package ahemcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PracticalQueryMode is the explicit material-retrieval route used by Detective.
const PracticalQueryMode = "practical_multisurface_lexical_v1"

// PracticalQuerySchema preserves the full versioned source and recovery trace.
const PracticalQuerySchema = "grounded-evidence-brief-v7"

// PracticalQueryInput selects a bounded, read-only evidence search. Empty outcome
// searches all outcomes; it never implies that every returned record is admitted.
type PracticalQueryInput struct {
	Question         string
	SourceID         string
	AdmissionOutcome string
	LifecycleScope   string
	Limit            int
}

// PracticalQueryResult retains the complete native JSON without discarding source
// references, states, retrieval basis, limitations or unknown additive fields.
// Counts and flags describe retrieval, not relevance or support for an answer.
type PracticalQueryResult struct {
	Response          json.RawMessage
	ReturnedMatches   int
	FallbackAttempted bool
	Truncated         bool
	HanTermPolicy     *PracticalHanTermPolicy
}

func supportsPracticalQuery(schema map[string]json.RawMessage) bool {
	var properties map[string]struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	}
	if json.Unmarshal(schema["properties"], &properties) != nil {
		return false
	}
	return properties["query"].Type == "string" &&
		properties["query_mode"].Type == "string" && slices.Contains(properties["query_mode"].Enum, PracticalQueryMode) &&
		properties["response_schema"].Type == "string" && slices.Contains(properties["response_schema"].Enum, PracticalQuerySchema)
}

func validPracticalInput(input PracticalQueryInput) bool {
	if strings.TrimSpace(input.Question) != input.Question || !utf8.ValidString(input.Question) || input.Question == "" || utf8.RuneCountInString(input.Question) > 256 || len(strings.Fields(input.Question)) > 16 || input.Limit < 1 || input.Limit > 100 || !slices.Contains([]string{"active", "historical", "all"}, input.LifecycleScope) || !slices.Contains([]string{"", "pending", "rejected", "audit_only", "admitted"}, input.AdmissionOutcome) {
		return false
	}
	if input.SourceID != "" && (!validText(input.SourceID, 500) || strings.TrimSpace(input.SourceID) != input.SourceID) {
		return false
	}
	for _, r := range input.Question + input.SourceID {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// SearchPractical performs exactly one Query MCP tool call after verifying the
// selected server and mode/schema inventory. It does not retry, call models,
// follow suggested tools, create pending proposals or record review decisions.
func SearchPractical(ctx context.Context, command string, input PracticalQueryInput) (PracticalQueryResult, error) {
	if ctx == nil || ctx.Err() != nil || !validPracticalInput(input) {
		return PracticalQueryResult{}, errors.New("practical query requires an active context, bounded question and exact supported filters")
	}
	c, err := startQuery(ctx, command)
	if err != nil {
		return PracticalQueryResult{}, err
	}
	c.queryBriefOnly = true
	defer c.Close()
	if err := c.initialize(); err != nil {
		return PracticalQueryResult{}, err
	}
	arguments := map[string]any{"query": input.Question, "query_mode": PracticalQueryMode, "response_schema": PracticalQuerySchema, "limit": input.Limit, "lifecycle_scope": input.LifecycleScope}
	if input.SourceID != "" {
		arguments["source_id"] = input.SourceID
	}
	if input.AdmissionOutcome != "" {
		arguments["admission_outcome"] = input.AdmissionOutcome
	}
	var raw json.RawMessage
	if err := c.call("get_grounded_evidence_brief", arguments, &raw); err != nil {
		return PracticalQueryResult{}, err
	}
	result, err := validatePracticalResponse(raw, input)
	if err != nil {
		return PracticalQueryResult{}, err
	}
	if err := c.Close(); err != nil {
		return PracticalQueryResult{}, errors.New("practical query launcher did not exit cleanly; no automatic retry was made")
	}
	return result, nil
}

func validatePracticalResponse(raw json.RawMessage, input PracticalQueryInput) (PracticalQueryResult, error) {
	var response struct {
		SchemaVersion string `json:"schema_version"`
		Query         string `json:"query"`
		Execution     struct {
			OriginalQuery  string          `json:"original_query"`
			QueryMode      string          `json:"query_mode"`
			Limit          int             `json:"limit"`
			CandidateCount int             `json:"candidate_count"`
			Truncated      bool            `json:"truncated"`
			GlobalAbsence  *bool           `json:"global_absence_inference_allowed"`
			Multisurface   json.RawMessage `json:"multisurface"`
			Filters        struct {
				SourceID         string `json:"source_id"`
				AdmissionOutcome string `json:"admission_outcome"`
				LifecycleScope   string `json:"lifecycle_scope"`
			} `json:"filters"`
			Recovery *struct {
				Attempted           *bool           `json:"fallback_attempted"`
				MinimumEnglishTerms int             `json:"configured_minimum_english_terms"`
				HanTermPolicy       json.RawMessage `json:"han_term_policy"`
			} `json:"practical_recovery"`
		} `json:"query_execution"`
		Counts struct {
			Returned int `json:"returned_matches"`
		} `json:"counts"`
		Matches []struct {
			Ref struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"record_ref"`
			Outcome    string            `json:"admission_outcome"`
			SourceRefs []json.RawMessage `json:"source_refs"`
			State      json.RawMessage   `json:"record_state"`
			Basis      json.RawMessage   `json:"retrieval_basis"`
		} `json:"matches"`
	}
	bad := errors.New("practical query response differs from the requested v7 contract; no result accepted and no retry made")
	if json.Unmarshal(raw, &response) != nil {
		return PracticalQueryResult{}, bad
	}
	e := response.Execution
	if response.SchemaVersion != PracticalQuerySchema || response.Query != input.Question || e.OriginalQuery != input.Question || e.QueryMode != PracticalQueryMode || e.Limit != input.Limit || e.Filters.SourceID != input.SourceID || e.Filters.AdmissionOutcome != input.AdmissionOutcome || e.Filters.LifecycleScope != input.LifecycleScope || e.GlobalAbsence == nil || *e.GlobalAbsence || !queryJSONObject(e.Multisurface) || e.Recovery == nil || e.Recovery.Attempted == nil || response.Matches == nil || response.Counts.Returned != len(response.Matches) || e.CandidateCount != len(response.Matches) || len(response.Matches) > input.Limit {
		return PracticalQueryResult{}, bad
	}
	minimum := 2
	if *e.Recovery.Attempted {
		minimum = 1
	}
	if e.Recovery.MinimumEnglishTerms != minimum {
		return PracticalQueryResult{}, bad
	}
	hanPolicy, err := parsePracticalHanTermPolicy(e.Recovery.HanTermPolicy)
	if err != nil {
		return PracticalQueryResult{}, bad
	}
	seen := make(map[string]bool)
	for _, match := range response.Matches {
		if match.Ref.Kind != "proposal" || !pendingID(match.Ref.ID, "occ:") || seen[match.Ref.ID] || !slices.Contains([]string{"pending", "rejected", "audit_only", "admitted"}, match.Outcome) || input.AdmissionOutcome != "" && match.Outcome != input.AdmissionOutcome || len(match.SourceRefs) == 0 || !queryJSONObject(match.State) || !queryJSONObject(match.Basis) {
			return PracticalQueryResult{}, bad
		}
		seen[match.Ref.ID] = true
	}
	return PracticalQueryResult{Response: append(json.RawMessage(nil), raw...), ReturnedMatches: len(response.Matches), FallbackAttempted: *e.Recovery.Attempted, Truncated: e.Truncated, HanTermPolicy: hanPolicy}, nil
}

func queryJSONObject(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil && len(fields) > 0
}
