package evidencequerymcp

import (
	"encoding/json"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
)

// ToolGetQueryContract publishes the versions a downstream consumer parses.
const ToolGetQueryContract = "get_query_contract"

// QueryContractV1 identifies the shape of QueryContract itself.
const QueryContractV1 = "ahe-query-contract/v1"

// SupportedProjectionVersions lists what governance tools can return. v1 is
// absent on purpose: it lacks the historical-derivation exclusion, so serving
// it again would weaken the current-use gate.
var SupportedProjectionVersions = []string{evidenceuse.ProjectionV2}

// QueryContract is what this binary would return. It is read without a
// database call, so it describes the binary, not the store it is attached to.
// Its encoding has no time or store field, so one binary always returns the
// same bytes and an operator can pin their digest.
type QueryContract struct {
	Contract           string   `json:"contract"`
	ProjectionVersions []string `json:"projection_versions"`
	UsePolicies        []string `json:"use_policies"`
	ExpectedMigration  string   `json:"expected_migration"`
	RolePolicy         string   `json:"role_policy"`
	Limitations        []string `json:"limitations"`
}

func queryContract() (QueryContract, error) {
	latest, err := migrations.LatestMigration()
	if err != nil {
		return QueryContract{}, err
	}
	return QueryContract{
		Contract:           QueryContractV1,
		ProjectionVersions: slices.Clone(SupportedProjectionVersions),
		UsePolicies:        []string{string(evidenceuse.CurrentActionV1), string(evidenceuse.GroundedActionV1)},
		ExpectedMigration:  latest,
		RolePolicy:         dbrole.PolicyVersion,
		Limitations: []string{
			"describes this binary; the attached database may be at another migration",
			"version agreement does not cover semantic preconditions such as initial lineage registration; follow reason codes in docs/CURRENT_USE_GUIDANCE.md",
		},
	}, nil
}

func contractTool() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func callQueryContract(payload []byte) ([]byte, error) {
	var req struct{}
	if err := decodeStrict(payload, &req); err != nil {
		return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
	}
	c, err := queryContract()
	if err != nil {
		return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
	}
	return json.Marshal(c)
}

// ServerInstructions is sent in the Query initialize result so that a model
// connected from any repository receives the same usage rules as the tools.
const ServerInstructions = `AHE Query is a read-only evidence layer. Results are evidence packages, not answers:
retrieval rank is not truth, and an empty result covers only the queried scope.

1. At connection, call get_query_contract and compare it with the versions your
   operator configured. On a mismatch, stop and report a contract mismatch.
2. To answer a question, start with get_grounded_evidence_brief or
   get_evidence_record; cite the returned record IDs and excerpts.
3. Before acting on evidence, call get_evidence_use_state with an explicit policy
   (current-action/v1 or grounded-current-action/v1). After planning, recheck every
   action's complete basis with check_current_evidence_plan under the same policy.
   An earlier pass, admission or cached result is not clearance.
4. A blocked or excluded node carries reason codes. Look them up in the reason code
   reference (docs/CURRENT_USE_GUIDANCE.md in the AHE repository). Some reasons need
   a separately authorized human review; this server cannot perform it.
5. This server cannot write, admit, approve or repair evidence. Never work around a
   refusal by changing policy, pins or profiles.`
