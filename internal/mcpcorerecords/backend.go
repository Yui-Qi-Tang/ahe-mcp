// Package mcpcorerecords exposes immutable external records and identity history.
// MCP requests never admit claims or execute a solver.
package mcpcorerecords

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Profile = "core-records"

// Backend binds a separately provisioned recorder to a launcher principal.
type Backend struct {
	pool      *pgxpool.Pool
	principal runtimeauth.Principal
}

// NewBackend constructs the write-capable recorder, never a query runtime.
func NewBackend(pool *pgxpool.Pool, principal runtimeauth.Principal) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("core records require PostgreSQL")
	}
	p, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	return &Backend{pool: pool, principal: p}, nil
}

// CallTool appends declared records and watch configurations. Identity is launcher-owned.
func (b *Backend) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("core recorder unavailable")
	}
	if ctx == nil {
		return nil, errors.New("context required")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var fields map[string]json.RawMessage
	if err := decodeRequest(args, &fields); err != nil {
		return nil, err
	}
	for key := range fields {
		if strings.EqualFold(key, "decision_by") || strings.EqualFold(key, "recorded_by") {
			return nil, runtimeauth.NewUnauthorizedError("recorder identity is launcher-owned")
		}
	}
	var value any
	var err error
	switch name {
	case "register_consistency_watch":
		var in evidenceingestion.ConsistencyWatchInput
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		in.RecordedBy = b.principal.ID
		value, err = evidenceingestion.RegisterConsistencyWatch(ctx, b.pool, in)
	case "bind_canonical_proposition":
		var in evidenceingestion.PropositionBindingInput
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		in.DecisionBy = b.principal.ID
		value, err = evidenceingestion.BindCanonicalProposition(ctx, b.pool, in)
	case "change_proposition_binding":
		var in evidenceingestion.PropositionBindingChange
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		in.DecisionBy = b.principal.ID
		value, err = evidenceingestion.ChangePropositionBinding(ctx, b.pool, in)
	case "record_external_check":
		var in evidenceingestion.ExternalCheckInput
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		in.RecordedBy = b.principal.ID
		value, err = evidenceingestion.RecordExternalCheck(ctx, b.pool, in)
	case "record_external_representation":
		var in evidenceingestion.ExternalRepresentationInput
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		in.RecordedBy = b.principal.ID
		value, err = evidenceingestion.RecordExternalRepresentation(ctx, b.pool, in)
	case "link_external_check_representation":
		var in evidenceingestion.ExternalRepresentationLink
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		var replayed bool
		replayed, err = evidenceingestion.LinkExternalCheckRepresentation(ctx, b.pool, in)
		value = struct {
			Link     evidenceingestion.ExternalRepresentationLink `json:"link"`
			Replayed bool                                         `json:"replayed"`
		}{in, replayed}
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation unavailable in core-records profile")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// IsReadTool recognizes only the closed, read-only Core record surface.
func IsReadTool(name string) bool {
	if isConsistencyRead(name) {
		return true
	}
	switch name {
	case "get_proposition_members", "get_proposition_binding_history", "get_external_check_subject", "get_external_check", "list_external_checks", "get_external_representation", "get_external_dependency_users", "get_external_representation_material":
		return true
	default:
		return false
	}
}

// CallRead reads exact records without changing their state or choosing a result.
func CallRead(ctx context.Context, pool *pgxpool.Pool, name string, args json.RawMessage) (json.RawMessage, error) {
	if ctx == nil || pool == nil {
		return nil, errors.New("context and PostgreSQL required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if isConsistencyRead(name) {
		return callConsistencyRead(ctx, pool, name, args)
	}
	var value any
	var err error
	switch name {
	case "get_proposition_members":
		var in struct {
			Key   evidenceingestion.PropositionKey `json:"key"`
			Limit int                              `json:"limit"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		value, err = evidenceingestion.ReadPropositionMembers(ctx, pool, in.Key, in.Limit)
	case "get_proposition_binding_history":
		var in struct {
			NodeID   string `json:"node_id"`
			Revision *int64 `json:"revision"`
			Limit    int    `json:"limit"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		if in.Revision == nil {
			return nil, errors.New("explicit revision required: -1 current, 0 initial, or an event revision")
		}
		value, err = evidenceingestion.ReadPropositionBindingHistory(ctx, pool, in.NodeID, *in.Revision, in.Limit)
	case "get_external_check_subject", "list_external_checks":
		var in struct {
			ProposalID string `json:"proposal_occurrence_id"`
			Limit      int    `json:"limit,omitempty"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		if name == "get_external_check_subject" {
			value, err = evidenceingestion.LoadExternalCheckSubject(ctx, pool, in.ProposalID)
		} else {
			value, err = evidenceingestion.ReadExternalChecks(ctx, pool, in.ProposalID, in.Limit)
		}
	case "get_external_check":
		var in struct {
			ID string `json:"check_id"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		value, err = evidenceingestion.ReadExternalCheck(ctx, pool, in.ID)
	case "get_external_representation", "get_external_representation_material":
		var in struct {
			ID      string `json:"representation_id"`
			InputID string `json:"input_id,omitempty"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		var record evidenceingestion.ExternalRepresentationRecord
		record, err = evidenceingestion.ReadExternalRepresentation(ctx, pool, in.ID)
		value = record
		if err == nil && name == "get_external_representation_material" {
			value, err = record.CheckMaterial(in.InputID)
		}
	case "get_external_dependency_users":
		var in struct {
			Key   evidenceingestion.ExternalDependencyKey `json:"key"`
			Limit int                                     `json:"limit"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		value, err = evidenceingestion.ReadExternalDependencyUsers(ctx, pool, in.Key, in.Limit)
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation unavailable in query profile")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
