package ahemcp

import (
	"encoding/json"
	"reflect"
	"slices"
)

// Compare consumed schema semantics, not descriptive prose or property order.
// Any unrecognized validation keyword is rejected rather than silently ignored.
func validReviewerSchema(name string, raw map[string]json.RawMessage) bool {
	data, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	var actual any
	if json.Unmarshal(data, &actual) != nil {
		return false
	}
	expected := reviewerSchema(name)
	if expected == nil {
		return false
	}
	expectedJSON, _ := json.Marshal(expected)
	var normalized any
	_ = json.Unmarshal(expectedJSON, &normalized)
	return reflect.DeepEqual(normalizeReviewSchema(actual), normalizeReviewSchema(normalized))
}

func normalizeReviewSchema(value any) any {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "description")
		for key, child := range v {
			v[key] = normalizeReviewSchema(child)
		}
		if list, ok := v["required"].([]any); ok {
			slices.SortFunc(list, func(a, b any) int {
				left, lok := a.(string)
				right, rok := b.(string)
				if !lok || !rok {
					return 0
				}
				if left < right {
					return -1
				}
				if left > right {
					return 1
				}
				return 0
			})
		}
		return v
	case []any:
		for i := range v {
			v[i] = normalizeReviewSchema(v[i])
		}
	}
	return value
}

func reviewerSchema(name string) map[string]any {
	id := func(prefix string) map[string]any {
		return map[string]any{"type": "string", "pattern": "^" + prefix + "[0-9a-f]{64}$", "minLength": len(prefix) + 64, "maxLength": len(prefix) + 64}
	}
	object := func(properties map[string]any) map[string]any {
		keys := make([]string, 0, len(properties))
		for k := range properties {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return map[string]any{"type": "object", "properties": properties, "required": keys, "additionalProperties": false}
	}
	if name == "get_source_claim_review" {
		return object(map[string]any{"extraction_attempt_id": id("attempt:"), "proposal_occurrence_id": id("occ:")})
	}
	decisions := []string{"approved"}
	if name == "record_reviewed_source_claim_disposition" {
		decisions = []string{"reject", "audit_only"}
	} else if name != "admit_reviewed_source_claim" {
		return nil
	}
	return object(map[string]any{
		"extraction_attempt_id": id("attempt:"),
		"expected_subject": object(map[string]any{
			"review_subject":             object(map[string]any{"submission_receipt_id": id("submission-receipt:v1:sha256:"), "proposal_manifest_id": id("proposal-manifest:v1:sha256:"), "proposal_occurrence_id": id("occ:"), "proposal_basis_id": id("proposal-basis:v1:sha256:"), "review_package_id": id("review-package:v1:sha256:")}),
			"review_display_artifact_id": id("review-display:v1:sha256:"),
		}),
		"decision":        map[string]any{"type": "string", "enum": decisions},
		"decision_reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
	})
}
