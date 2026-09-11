package mcpquery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

// QueryRuntimeAuthorizationVersion identifies the local stdio authorization
// contract: one launcher principal and one verified PostgreSQL schema cut per
// process.
const QueryRuntimeAuthorizationVersion = "query-runtime-authorization/v1"

const queryReadViewOwnerDomainV1 = "ahe-query-read-view-owner/v1"

// QueryVisibilityCut declares the complete persisted visibility boundary for
// one local query process when the trusted composition root constructs it from
// that process's OpenRuntimePool result. Different cuts must use different
// process and pool instances; tool payloads cannot replace any field.
type QueryVisibilityCut struct {
	Database     string         `json:"database"`
	Schema       string         `json:"schema"`
	DatabaseRole string         `json:"database_role"`
	Profile      dbrole.Profile `json:"profile"`
	ManifestHash string         `json:"manifest_hash"`
}

// QueryRuntimeBinding is a credential-free witness of the launcher identity
// and visibility cut enforced by one query process.
type QueryRuntimeBinding struct {
	SchemaVersion string             `json:"schema_version"`
	PrincipalID   string             `json:"principal_id"`
	VisibilityCut QueryVisibilityCut `json:"visibility_cut"`
}

// QueryRuntimeAuthorization is immutable after construction. It deliberately
// contains no request-derived principal, tenant, source namespace, or scope.
type QueryRuntimeAuthorization struct {
	principal     runtimeauth.Principal
	binding       QueryRuntimeBinding
	readViewOwner string
}

var queryRuntimeToolNames = []string{
	evidencequerymcp.ToolGetEvidenceRecord,
	evidencequerymcp.ToolListEvidenceRecords,
	evidencequerymcp.ToolSearchEvidenceRecords,
	evidencequerymcp.ToolGetGroundedEvidenceBrief,
	evidencequerymcp.ToolListEvidenceNeighbors,
	evidencequerymcp.ToolGetRelationProvenance,
	evidencequerymcp.ToolGetMCPReadSourceStates,
	evidencequerymcp.ToolOpenCanonicalReadView,
	evidencequerymcp.ToolFindCanonicalPath,
	evidencequerymcp.ToolGetCanonicalTopologyDiagnostics,
	evidencequerymcp.ToolGetCanonicalContradictionProposal,
	evidencequerymcp.ToolGetCanonicalSupersessionHead,
	evidencequerymcp.ToolGetCanonicalSupersessionCurrentness,
}

// NewQueryRuntimeAuthorization binds one exact launcher identity to a
// structurally valid query database-role witness. The trusted composition root
// must supply the witness returned with the backend's own pool. The database
// profile fixes mutation capability to none for this query process; the
// database/schema pair supplies the persisted visibility cut.
func NewQueryRuntimeAuthorization(
	principalID string,
	databaseBinding dbrole.RuntimeBinding,
) (*QueryRuntimeAuthorization, error) {
	principal, err := runtimeauth.NewPrincipal(principalID)
	if err != nil {
		return nil, err
	}
	if principal.ID != principalID {
		return nil, errors.New("AHE_RUNTIME_PRINCIPAL_ID must not have surrounding whitespace")
	}
	if err := dbrole.ValidateRuntimeBinding(databaseBinding, dbrole.ProfileQuery); err != nil {
		return nil, fmt.Errorf("validating query database runtime binding: %w", err)
	}
	binding := QueryRuntimeBinding{
		SchemaVersion: QueryRuntimeAuthorizationVersion,
		PrincipalID:   principal.ID,
		VisibilityCut: QueryVisibilityCut{
			Database:     databaseBinding.Database,
			Schema:       databaseBinding.Schema,
			DatabaseRole: databaseBinding.Role,
			Profile:      databaseBinding.Profile,
			ManifestHash: databaseBinding.ManifestHash,
		},
	}
	readViewOwner, err := queryRuntimeReadViewOwner(binding)
	if err != nil {
		return nil, err
	}
	return &QueryRuntimeAuthorization{
		principal:     principal,
		binding:       binding,
		readViewOwner: readViewOwner,
	}, nil
}

func queryRuntimeReadViewOwner(binding QueryRuntimeBinding) (string, error) {
	data, err := json.Marshal(binding)
	if err != nil {
		return "", fmt.Errorf("encoding query read-view owner: %w", err)
	}
	digestInput := make([]byte, 0, len(queryReadViewOwnerDomainV1)+1+len(data))
	digestInput = append(digestInput, queryReadViewOwnerDomainV1...)
	digestInput = append(digestInput, 0)
	digestInput = append(digestInput, data...)
	digest := sha256.Sum256(digestInput)
	return "query-read-view-owner:" + hex.EncodeToString(digest[:]), nil
}

// Principal returns the exact identity fixed by the trusted launcher.
func (a *QueryRuntimeAuthorization) Principal() runtimeauth.Principal {
	if a == nil {
		return runtimeauth.Principal{}
	}
	return a.principal
}

// Binding returns a value copy suitable for startup diagnostics and audit.
func (a *QueryRuntimeAuthorization) Binding() QueryRuntimeBinding {
	if a == nil {
		return QueryRuntimeBinding{}
	}
	return a.binding
}

// AuthorizedQueryBackend exposes only the exact closed read-only registry
// associated with the launcher-bound query process.
type AuthorizedQueryBackend struct {
	backend       mcpstdio.Backend
	tools         []mcpstdio.Tool
	allowed       map[string]struct{}
	binding       QueryRuntimeBinding
	readViewOwner string
}

// NewAuthorizedQueryBackend fails startup if the wrapped backend adds,
// removes, duplicates, or changes the read-only classification of a tool.
func NewAuthorizedQueryBackend(
	backend mcpstdio.Backend,
	authorization *QueryRuntimeAuthorization,
) (*AuthorizedQueryBackend, error) {
	if backend == nil {
		return nil, errors.New("query backend is required")
	}
	if authorization == nil {
		return nil, errors.New("query runtime authorization is required")
	}
	expected, err := queryToolRegistry()
	if err != nil {
		return nil, err
	}
	tools := backend.Tools()
	if len(tools) != len(expected) {
		return nil, fmt.Errorf(
			"query backend tool inventory mismatch: got %d, want %d",
			len(tools),
			len(expected),
		)
	}
	allowed := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if _, duplicate := allowed[tool.Name]; duplicate {
			return nil, fmt.Errorf("query backend exposes duplicate tool %q", tool.Name)
		}
		if _, registered := expected[tool.Name]; !registered {
			return nil, fmt.Errorf("query tool %q has no runtime authorization classification", tool.Name)
		}
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			return nil, fmt.Errorf("query tool %q is not classified read-only", tool.Name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			return nil, fmt.Errorf("query tool %q is not classified non-destructive", tool.Name)
		}
		allowed[tool.Name] = struct{}{}
	}
	for name := range expected {
		if _, present := allowed[name]; !present {
			return nil, fmt.Errorf("registered query tool %q is absent from the backend", name)
		}
	}
	return &AuthorizedQueryBackend{
		backend:       backend,
		tools:         append([]mcpstdio.Tool(nil), tools...),
		allowed:       allowed,
		binding:       authorization.Binding(),
		readViewOwner: authorization.readViewOwner,
	}, nil
}

func queryToolRegistry() (map[string]struct{}, error) {
	if len(queryRuntimeToolNames) == 0 {
		return nil, errors.New("query tool registry is empty")
	}
	registry := make(map[string]struct{}, len(queryRuntimeToolNames))
	for _, name := range queryRuntimeToolNames {
		if name == "" {
			return nil, errors.New("query tool registry contains an empty name")
		}
		if _, duplicate := registry[name]; duplicate {
			return nil, fmt.Errorf("query tool registry repeats tool %q", name)
		}
		registry[name] = struct{}{}
	}
	return registry, nil
}

// Tools returns the exact authorized read-only surface.
func (b *AuthorizedQueryBackend) Tools() []mcpstdio.Tool {
	return append([]mcpstdio.Tool(nil), b.tools...)
}

// Binding returns the launcher and persisted visibility witness for this
// backend without exposing database credentials.
func (b *AuthorizedQueryBackend) Binding() QueryRuntimeBinding {
	if b == nil {
		return QueryRuntimeBinding{}
	}
	return b.binding
}

// CallTool rejects all names outside the closed registry before the wrapped
// backend can observe the request.
func (b *AuthorizedQueryBackend) CallTool(
	ctx context.Context,
	name string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	if _, allowed := b.allowed[name]; !allowed {
		return nil, runtimeauth.NewUnauthorizedError("tool is not available in this query runtime")
	}
	ownedCtx, err := evidencequerymcp.BindCanonicalReadViewOwner(ctx, b.readViewOwner)
	if err != nil {
		return nil, runtimeauth.NewUnauthorizedError("query runtime read-view owner binding is invalid")
	}
	return b.backend.CallTool(ownedCtx, name, arguments)
}
