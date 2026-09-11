package ahemcp

import (
	"encoding/json"
	"errors"
	"slices"
	"unicode"
)

// PracticalHanTermPolicy projects optional v7 metadata for the Han supplemental
// search only. It does not describe English/baseline admission to the result set
// or establish that the model understood a topic or found semantic support.
type PracticalHanTermPolicy struct {
	Version                     string   `json:"version"`
	AuxiliaryTerms              []string `json:"auxiliary_terms"`
	EligibleAnchorTerms         []string `json:"eligible_anchor_terms"`
	AuxiliaryOnlyQueryPreserved bool     `json:"auxiliary_only_query_preserved"`
}

func parsePracticalHanTermPolicy(raw json.RawMessage) (*PracticalHanTermPolicy, error) {
	// Older v7 servers do not have the optional policy. Explicit null or a
	// partial new policy must not masquerade as that compatible absence.
	if len(raw) == 0 {
		return nil, nil
	}
	bad := errors.New("invalid practical Han term policy")
	var fields map[string]json.RawMessage
	var policy PracticalHanTermPolicy
	var preserved *bool
	if json.Unmarshal(raw, &fields) != nil || fields == nil ||
		json.Unmarshal(fields["version"], &policy.Version) != nil || policy.Version != "han-auxiliary-anchor-v1" ||
		json.Unmarshal(fields["auxiliary_terms"], &policy.AuxiliaryTerms) != nil || policy.AuxiliaryTerms == nil ||
		json.Unmarshal(fields["eligible_anchor_terms"], &policy.EligibleAnchorTerms) != nil || policy.EligibleAnchorTerms == nil ||
		json.Unmarshal(fields["auxiliary_only_query_preserved"], &preserved) != nil || preserved == nil {
		return nil, bad
	}
	policy.AuxiliaryOnlyQueryPreserved = *preserved
	auxiliary := []string{"影響", "影响", "造成", "導致", "导致", "發生", "发生", "是否", "哪些", "什麼", "什么", "多少", "如何"}
	seen := make(map[string]bool)
	for _, term := range policy.AuxiliaryTerms {
		if !slices.Contains(auxiliary, term) || seen[term] {
			return nil, bad
		}
		seen[term] = true
	}
	seen = make(map[string]bool)
	for _, term := range policy.EligibleAnchorTerms {
		runes := []rune(term)
		if len(runes) != 2 || !unicode.Is(unicode.Han, runes[0]) || !unicode.Is(unicode.Han, runes[1]) || seen[term] ||
			!policy.AuxiliaryOnlyQueryPreserved && slices.Contains(auxiliary, term) {
			return nil, bad
		}
		seen[term] = true
	}
	if policy.AuxiliaryOnlyQueryPreserved {
		if len(policy.AuxiliaryTerms) == 0 || len(policy.EligibleAnchorTerms) != len(policy.AuxiliaryTerms) {
			return nil, bad
		}
		for _, term := range policy.AuxiliaryTerms {
			if !seen[term] {
				return nil, bad
			}
		}
	}
	return &policy, nil
}
