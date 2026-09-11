package labstatus

import (
	"fmt"

	"google.golang.org/genai"
)

var (
	recordTypes = []string{
		"metadata",
		"capability_state",
		"verification_result",
		"interface_inventory",
		"authority_boundary",
		"release_gate",
		"uncertainty",
		"non_goal",
	}
	epistemicClasses = []string{"claim", "unknown", "blocked"}
	statuses         = []string{
		"not_applicable",
		"represented",
		"implemented",
		"implemented_exposed",
		"internal_not_exposed",
		"partial",
		"lab_proven",
		"unreleased_lab_proven",
		"unreleased_core_proven",
		"released",
		"closed",
		"open",
		"backlog",
		"experiment_ready",
		"deferred_non_goal",
		"regression",
		"not_claimed",
		"optional_experimental",
		"passed",
		"skipped",
		"not_run",
		"unknown",
	}
	scopes = []string{
		"document",
		"schema_representation",
		"runtime_core",
		"stdio",
		"lab_contract",
		"lab_postgresql_prototype",
		"source_tree_verification",
		"ahe_wrap_query_mcp",
		"ahe_mcp",
		"named_deployment",
		"operation",
		"external_connector",
	}
	selectionStates = []string{"selected", "not_selected", "deferred", "unspecified"}
)

// OutputSchema returns the bounded schema supplied to the ADK model.
func OutputSchema() *genai.Schema {
	shortString := func(description string) *genai.Schema {
		return &genai.Schema{
			Type:        genai.TypeString,
			Description: description,
			MinLength:   int64Pointer(1),
		}
	}
	stringList := func(description string, maximum int64) *genai.Schema {
		return &genai.Schema{
			Type:        genai.TypeArray,
			Description: description + " The controller enforces a maximum of " + fmt.Sprint(maximum) + " items.",
			Items:       shortString("One bounded item."),
			MaxItems:    int64Pointer(maximum),
		}
	}
	optionalString := func(description string) *genai.Schema {
		return &genai.Schema{
			Type:        genai.TypeString,
			Description: description,
		}
	}
	enum := func(values []string, description string) *genai.Schema {
		return &genai.Schema{
			Type:        genai.TypeString,
			Format:      "enum",
			Enum:        values,
			Description: description,
		}
	}

	citation := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"start_line": {
				Type:        genai.TypeInteger,
				Description: "Inclusive one-based first source line.",
				Minimum:     float64Pointer(1),
			},
			"end_line": {
				Type:        genai.TypeInteger,
				Description: "Inclusive one-based final source line.",
				Minimum:     float64Pointer(1),
			},
		},
		Required: []string{"start_line", "end_line"},
	}

	record := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"record_type":        enum(recordTypes, "Kind of atomic status assertion."),
			"subject":            shortString("Stable, concise subject name."),
			"statement":          shortString("Self-contained proposition reporting what the source asserts about the subject, status, scope, substantive capability and necessary limitations; never just the subject name or a heading. Not independent verification."),
			"epistemic_class":    enum(epistemicClasses, "Document claim, explicit unknown, or named blocked promotion."),
			"status":             enum(statuses, "Status without upgrading the document assertion."),
			"scope":              enum(scopes, "Layer or authority boundary to which this record applies."),
			"selection_state":    enum(selectionStates, "Whether the source selected this work as next work."),
			"citation":           citation,
			"blocked_by":         stringList("Explicit prerequisites named by the source.", 24),
			"does_not_establish": stringList("Stronger conclusions explicitly not established.", 24),
			"qualifiers":         stringList("Dates, counts, environments, or other exact qualifiers.", 32),
		},
		Required: []string{
			"record_type",
			"subject",
			"statement",
			"epistemic_class",
			"status",
			"scope",
			"selection_state",
			"citation",
			"blocked_by",
			"does_not_establish",
			"qualifiers",
		},
	}

	abstention := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"subject": shortString("The bounded topic not extracted."),
			"reason":  shortString("Why the source does not support a record."),
		},
		Required: []string{"subject", "reason"},
	}

	return &genai.Schema{
		Type:        genai.TypeObject,
		Description: "Source-grounded candidate status records; never canonical evidence or admission.",
		Properties: map[string]*genai.Schema{
			"outcome": enum([]string{"extracted", "abstained"}, "Whether any grounded records were extracted."),
			"records": {
				Type:        genai.TypeArray,
				Description: "Atomic records; the controller enforces a maximum of 256 items.",
				Items:       record,
				MaxItems:    int64Pointer(256),
			},
			"abstentions": {
				Type:        genai.TypeArray,
				Description: "Bounded abstentions; the controller enforces a maximum of 128 items.",
				Items:       abstention,
				MaxItems:    int64Pointer(128),
			},
			"limitations":       stringList("Document or extraction limitations that affect interpretation.", 32),
			"abstention_reason": optionalString("Required and non-empty only when outcome is abstained; otherwise use an empty string."),
		},
		Required: []string{"outcome", "records", "abstentions", "limitations", "abstention_reason"},
	}
}

func int64Pointer(value int64) *int64       { return &value }
func float64Pointer(value float64) *float64 { return &value }
