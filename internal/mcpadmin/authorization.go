package mcpadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

// RuntimeProfile selects one exact ingestion capability set. Legacy profiles
// retain the shipping review workflow, not an exact-reviewed-only contract.
type RuntimeProfile string

const (
	RuntimeProfileIntake              RuntimeProfile = "intake"
	RuntimeProfileRelationReviewer    RuntimeProfile = "relation-reviewer"
	RuntimeProfileEndpointReviewer    RuntimeProfile = "endpoint-reviewer"
	RuntimeProfileRepositoryIntake    RuntimeProfile = "repository-intake"
	RuntimeProfileSourceClaimReviewer RuntimeProfile = "source-claim-reviewer"
	RuntimeProfileLegacyReviewer      RuntimeProfile = "legacy-reviewer"
	RuntimeProfileLegacyOperator      RuntimeProfile = "legacy-operator"
	maxAuthorizedArgumentsBytes                      = 16 << 20
)

// ParseRuntimeProfile rejects absent, unknown, or normalized-by-guess profiles.
func ParseRuntimeProfile(value string) (RuntimeProfile, error) {
	switch profile := RuntimeProfile(value); profile {
	case RuntimeProfileIntake, RuntimeProfileSourceClaimReviewer, RuntimeProfileRelationReviewer, RuntimeProfileEndpointReviewer, RuntimeProfileRepositoryIntake, RuntimeProfileLegacyReviewer, RuntimeProfileLegacyOperator:
		return profile, nil
	default:
		return "", runtimeauth.NewUnauthorizedError("an explicit supported ingestion runtime profile is required")
	}
}

// RuntimeProfileNames returns the exact supported ingestion profile names.
func RuntimeProfileNames() []string {
	return []string{string(RuntimeProfileIntake), string(RuntimeProfileSourceClaimReviewer), string(RuntimeProfileLegacyReviewer), string(RuntimeProfileLegacyOperator), string(RuntimeProfileRelationReviewer), string(RuntimeProfileEndpointReviewer), string(RuntimeProfileRepositoryIntake)}
}

// AuthorizedBackend binds one immutable launcher identity and capability set.
// It must wrap the full shipping ingestion registry. DB role isolation is a
// separate companion; this wrapper alone is not a database permission proof.
type AuthorizedBackend struct {
	backend   mcpstdio.Backend
	principal runtimeauth.Principal
	profile   RuntimeProfile
	toolsJSON []byte
	allowed   map[string]ingestionAuthorityClass
	fields    map[string]map[string]struct{}
}

// NewAuthorizedBackend validates the complete native registry before filtering
// it. Unknown, missing, duplicate, or schema-drifted tools stop startup.
func NewAuthorizedBackend(backend mcpstdio.Backend, principal runtimeauth.Principal, profile RuntimeProfile) (*AuthorizedBackend, error) {
	if backend == nil || (reflect.ValueOf(backend).Kind() == reflect.Pointer && reflect.ValueOf(backend).IsNil()) {
		return nil, errors.New("ingestion backend is required")
	}
	validated, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	if _, err := ParseRuntimeProfile(string(profile)); err != nil {
		return nil, err
	}
	if profile == RuntimeProfileRelationReviewer || profile == RuntimeProfileEndpointReviewer || profile == RuntimeProfileRepositoryIntake {
		return nil, errors.New("relation-reviewer requires its dedicated native backend")
	}
	if profile == RuntimeProfileSourceClaimReviewer {
		bound, ok := backend.(interface{ SourceClaimReviewerPrincipal() runtimeauth.Principal })
		if !ok || bound.SourceClaimReviewerPrincipal() != validated {
			return nil, runtimeauth.NewUnauthenticatedError("reviewed writer and runtime must share one launcher principal")
		}
	}
	native, incoming := ingestionTools(), backend.Tools()
	if len(native) != 43 || len(incoming) != len(native) {
		return nil, errors.New("ingestion tool inventory differs from the reviewed contract")
	}
	expected := make(map[string][]byte, len(native))
	for _, tool := range native {
		if ingestionAuthorityForTool(tool.Name) == authorityUnknown {
			return nil, errors.New("ingestion tool inventory has an unclassified operation")
		}
		encoded, err := json.Marshal(tool)
		if err != nil {
			return nil, errors.New("ingestion native tool contract cannot be encoded")
		}
		expected[tool.Name] = encoded
	}
	b := &AuthorizedBackend{backend: backend, principal: validated, profile: profile,
		allowed: make(map[string]ingestionAuthorityClass), fields: make(map[string]map[string]struct{})}
	selected := make([]mcpstdio.Tool, 0, len(native))
	seen := make(map[string]bool, len(native))
	for _, tool := range incoming {
		encoded, err := json.Marshal(tool)
		want, known := expected[tool.Name]
		if err != nil || !known || seen[tool.Name] || !bytes.Equal(encoded, want) {
			return nil, errors.New("ingestion tool inventory or schema differs from the reviewed contract")
		}
		seen[tool.Name] = true
		class := ingestionAuthorityForTool(tool.Name)
		if !profileAllows(profile, class) {
			continue
		}
		// Detach nested schema maps and annotation pointers before changing the
		// advertised decision contract or returning tool metadata to a caller.
		var detached mcpstdio.Tool
		if err := json.Unmarshal(encoded, &detached); err != nil {
			return nil, errors.New("ingestion tool contract cannot be retained")
		}
		if class == authorityDecision {
			properties, ok := detached.InputSchema["properties"].(map[string]any)
			if !ok {
				return nil, errors.New("ingestion decision contract has no exact properties")
			}
			if tool.Name == evidenceingestionmcp.ToolAdmitPendingProposal {
				// The shipping typed request accepts derivation, although the
				// original stdio schema omits it. Preserve that existing contract
				// explicitly; this does not enable admission for intake runtimes.
				properties["derivation"] = objectSchema(map[string]any{
					"parent_node_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"method":          stringSchema("Existing typed derivation method."),
					"producer":        stringSchema("Existing typed derivation producer, not the admission decision identity."),
					"trace_ref":       stringSchema("Existing typed derivation trace reference."),
				}, []string{"parent_node_ids", "method", "producer", "trace_ref"})
			}
			fields := make(map[string]struct{}, len(properties))
			for field := range properties {
				fields[field] = struct{}{}
			}
			b.fields[tool.Name] = fields
			properties["decision_by"] = map[string]any{"type": "string", "minLength": 1, "maxLength": runtimeauth.PrincipalIDMaxBytes,
				"description": "Optional exact echo of the launcher principal; omitted values are supplied by the runtime. A different identity is rejected."}
			required, _ := detached.InputSchema["required"].([]any)
			filtered := make([]any, 0, len(required))
			for _, field := range required {
				if field != "decision_by" {
					filtered = append(filtered, field)
				}
			}
			detached.InputSchema["required"] = filtered
			detached.Description += " This legacy runtime binds decision_by to the launcher principal; it does not implement exact-reviewed-only admission or authenticate a human."
		}
		selected = append(selected, detached)
		b.allowed[tool.Name] = class
	}
	b.toolsJSON, err = json.Marshal(selected)
	if err != nil {
		return nil, errors.New("authorized ingestion tool contract cannot be retained")
	}
	return b, nil
}

// Principal returns the fixed launcher identity, not an identity from a call.
func (b *AuthorizedBackend) Principal() runtimeauth.Principal { return b.principal }

// Profile returns the exact launcher-selected ingestion profile.
func (b *AuthorizedBackend) Profile() RuntimeProfile { return b.profile }

// Tools returns detached metadata for only the authorized capability set.
func (b *AuthorizedBackend) Tools() []mcpstdio.Tool {
	if b == nil {
		return nil
	}
	var tools []mcpstdio.Tool
	if json.Unmarshal(b.toolsJSON, &tools) != nil {
		return nil // An unavailable retained contract never exposes tools.
	}
	return tools
}

// CallTool checks the fixed capability set before the backend sees arguments.
// Decision calls accept no replacement for the launcher-derived identity.
func (b *AuthorizedBackend) CallTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.backend == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("ingestion runtime identity is unavailable")
	}
	class, allowed := b.allowed[name]
	if !allowed {
		return nil, runtimeauth.NewUnauthorizedError("operation is unavailable in this ingestion runtime")
	}
	if ctx == nil {
		return nil, runtimeauth.NewUnauthorizedError("request context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if class == authorityDecision {
		var err error
		arguments, err = b.bindDecisionPrincipal(name, arguments)
		if err != nil {
			return nil, err
		}
	}
	return b.backend.CallTool(ctx, name, arguments)
}

func (b *AuthorizedBackend) bindDecisionPrincipal(name string, arguments json.RawMessage) (json.RawMessage, error) {
	rejected := func() (json.RawMessage, error) {
		return nil, runtimeauth.NewUnauthorizedError("decision arguments must be one bounded exact object with the launcher identity")
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if len(arguments) > maxAuthorizedArgumentsBytes || !utf8.Valid(arguments) {
		return rejected()
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return rejected()
	}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok {
			return rejected()
		}
		if _, allowed := b.fields[name][field]; !allowed {
			return rejected()
		}
		if _, duplicate := values[field]; duplicate {
			return rejected()
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return rejected()
		}
		values[field] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return rejected()
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) != io.EOF {
		return rejected()
	}
	if value, supplied := values["decision_by"]; supplied {
		var id string
		if json.Unmarshal(value, &id) != nil || id != b.principal.ID {
			return rejected()
		}
	}
	values["decision_by"], err = json.Marshal(b.principal.ID)
	if err != nil {
		return rejected()
	}
	bound, err := json.Marshal(values)
	if err != nil {
		return rejected()
	}
	return bound, nil
}

type ingestionAuthorityClass uint8

const (
	authorityUnknown ingestionAuthorityClass = iota
	authorityIntake
	authorityDecision
	authorityRelationProposal
	authorityOperator
	authoritySourceClaimReview
)

func profileAllows(profile RuntimeProfile, class ingestionAuthorityClass) bool {
	switch profile {
	case RuntimeProfileIntake:
		return class == authorityIntake
	case RuntimeProfileSourceClaimReviewer:
		return class == authoritySourceClaimReview
	case RuntimeProfileLegacyReviewer:
		return class == authorityDecision || class == authorityRelationProposal
	case RuntimeProfileLegacyOperator:
		return class != authorityUnknown && class != authoritySourceClaimReview
	default:
		return false
	}
}

func ingestionAuthorityForTool(name string) ingestionAuthorityClass {
	switch name {
	case evidenceingestionmcp.ToolGetSourceClaimReview, evidenceingestionmcp.ToolAdmitReviewedSourceClaim, evidenceingestionmcp.ToolRecordReviewedSourceClaimDisposition:
		return authoritySourceClaimReview
	case evidenceingestionmcp.ToolSubmitManualEvidence, evidenceingestionmcp.ToolSubmitTextSource,
		evidenceingestionmcp.ToolSubmitExternalSource, evidenceingestionmcp.ToolSubmitExtractorOutput,
		evidenceingestionmcp.ToolGetExtractorInput:
		return authorityIntake
	case evidenceingestionmcp.ToolAdmitPendingProposal, evidenceingestionmcp.ToolRecordPendingProposalDisposition,
		evidenceingestionmcp.ToolAdmitPendingCanonicalContradiction, evidenceingestionmcp.ToolRecordPendingCanonicalContradictionDisposition,
		evidenceingestionmcp.ToolAdmitPendingSupersession:
		return authorityDecision
	case evidenceingestionmcp.ToolSubmitCanonicalContradictionProposal:
		return authorityRelationProposal
	case evidenceingestionmcp.ToolInspectGoplsWorkspace, evidenceingestionmcp.ToolInspectGitRepositoryChange,
		evidenceingestionmcp.ToolObserveGitRepositoryChange, evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork, evidenceingestionmcp.ToolRenewGitRepositoryExtractionWorkLease,
		evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork, evidenceingestionmcp.ToolRunGitRepositoryExtractionWorkerTick,
		evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkClaims, evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkExecutions,
		evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, evidenceingestionmcp.ToolRecoverExpiredGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution, evidenceingestionmcp.ToolRetryFailedGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolCaptureGitRepositorySnapshot, evidenceingestionmcp.ToolGetRepositoryExtractorInput,
		evidenceingestionmcp.ToolCreateRepositoryExtractionRun, evidenceingestionmcp.ToolRunRepositoryGoParserExtractor,
		evidenceingestionmcp.ToolRunRepositoryGoplsExtractor, evidenceingestionmcp.ToolListRepositorySourceGenerations,
		evidenceingestionmcp.ToolActivateRepositorySourceGeneration, evidenceingestionmcp.ToolRunLocalOllamaExtractor,
		evidenceingestionmcp.ToolRunGoParserExtractor, evidenceingestionmcp.ToolRunGoplsExtractor,
		evidenceingestionmcp.ToolClassifyFailedGitRepositoryExtractionWork, evidenceingestionmcp.ToolListDueGitRepositoryExtractionWorkRetryDecisions,
		evidenceingestionmcp.ToolConsumeDueGitRepositoryExtractionWorkRetryDecision, evidenceingestionmcp.ToolRunDueGitRepositoryExtractionWorkRetryControllerTick,
		evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick:
		return authorityOperator
	default:
		return authorityUnknown
	}
}
