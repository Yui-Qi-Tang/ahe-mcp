package evidenceingestion

import (
	"strings"
	"testing"
)

func TestPrepareMCPReadSourceStateQuery(t *testing.T) {
	sourceBindingID := "workspace-source:" + strings.Repeat("a", 64)
	tests := []struct {
		name  string
		input MCPReadSourceStateQueryInput
		mode  string
		limit int
		kind  ErrorKind
	}{
		{
			name: "latest observed",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeLatestObserved,
			},
			mode:  MCPReadSourceStateModeLatestObserved,
			limit: 1,
		},
		{
			name: "exact revision",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeExactRevision,
				Revision:        " revision-a ",
			},
			mode:  MCPReadSourceStateModeExactRevision,
			limit: 1,
		},
		{
			name: "history",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeHistory,
				Limit:           7,
			},
			mode:  MCPReadSourceStateModeHistory,
			limit: 7,
		},
		{
			name: "compare",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeCompare,
				FromRevision:    "revision-a",
				ToRevision:      "revision-b",
			},
			mode:  MCPReadSourceStateModeCompare,
			limit: 2,
		},
		{
			name: "invalid hash ID",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: "workspace-source:" + strings.Repeat("z", 64),
				Mode:            MCPReadSourceStateModeLatestObserved,
			},
			kind: ErrorInvalidRecordID,
		},
		{
			name: "exact without revision",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeExactRevision,
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "revision outside exact mode",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeHistory,
				Revision:        "revision-a",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "same compare revision",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeCompare,
				FromRevision:    "revision-a",
				ToRevision:      "revision-a",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "limit outside history",
			input: MCPReadSourceStateQueryInput{
				SourceBindingID: sourceBindingID,
				Mode:            MCPReadSourceStateModeLatestObserved,
				Limit:           1,
			},
			kind: ErrorInvalidInput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := prepareMCPReadSourceStateQuery(test.input)
			if test.kind != "" {
				kind, ok := KindOf(err)
				if !ok || kind != test.kind {
					t.Fatalf("error = %v, kind = %q, want %q", err, kind, test.kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepareMCPReadSourceStateQuery() error = %v", err)
			}
			if got.mode != test.mode || got.limit != test.limit {
				t.Fatalf("prepared query = %+v, want mode=%s limit=%d", got, test.mode, test.limit)
			}
		})
	}
}
