// Package detective implements bounded evidence-source orchestration control-plane state.
package detective

import (
	"errors"
	"fmt"
)

// ErrorKind identifies a typed detective control-plane failure.
type ErrorKind string

const (
	// ErrorInvalidInput means required registry input is absent or malformed.
	ErrorInvalidInput ErrorKind = "invalid_input"
	// ErrorUnsupportedCapability means a source capability name or version is not compiled in.
	ErrorUnsupportedCapability ErrorKind = "unsupported_capability"
	// ErrorIdempotencyKeyReused means a request ID was reused with a different payload.
	ErrorIdempotencyKeyReused ErrorKind = "idempotency_key_reused"
	// ErrorWorkspaceConflict means an immutable workspace identity maps to different registry material.
	ErrorWorkspaceConflict ErrorKind = "workspace_conflict"
	// ErrorWorkspaceNotFound means a requested workspace identity is not registered.
	ErrorWorkspaceNotFound ErrorKind = "workspace_not_found"
	// ErrorWorkspaceUnavailable means registered local paths are no longer safely resolvable.
	ErrorWorkspaceUnavailable ErrorKind = "workspace_unavailable"
	// ErrorSourceBindingNotFound means a requested source identity is not registered in the run workspace.
	ErrorSourceBindingNotFound ErrorKind = "source_binding_not_found"
	// ErrorOrchestrationRunNotFound means a requested detective run is not registered.
	ErrorOrchestrationRunNotFound ErrorKind = "orchestration_run_not_found"
	// ErrorOrchestrationRunConflict means a run cannot accept the requested state transition.
	ErrorOrchestrationRunConflict ErrorKind = "orchestration_run_conflict"
	// ErrorOrchestrationStepNotFound means a requested detective step is not registered in its run.
	ErrorOrchestrationStepNotFound ErrorKind = "orchestration_step_not_found"
	// ErrorPlannerInvocationFailed means the bounded local planner could not return output.
	ErrorPlannerInvocationFailed ErrorKind = "planner_invocation_failed"
	// ErrorPlannerMalformedOutput means planner output did not satisfy the strict versioned JSON shape.
	ErrorPlannerMalformedOutput ErrorKind = "planner_malformed_output"
	// ErrorPlannerContradictoryOutput means planner fields cannot be true together.
	ErrorPlannerContradictoryOutput ErrorKind = "planner_contradictory_output"
	// ErrorPlannerInsufficientCoverage means the audit cannot ground a planner recommendation.
	ErrorPlannerInsufficientCoverage ErrorKind = "planner_insufficient_coverage"
	// ErrorPlannerUngroundedOutput means planner output does not map to an eligible registered source.
	ErrorPlannerUngroundedOutput ErrorKind = "planner_ungrounded_output"
	// ErrorConnectorDeliveryConflict means one connector delivery identity maps to different immutable bytes.
	ErrorConnectorDeliveryConflict ErrorKind = "connector_delivery_conflict"
	// ErrorConnectorDeliveryNotFound means a requested durable connector delivery does not exist.
	ErrorConnectorDeliveryNotFound ErrorKind = "connector_delivery_not_found"
	// ErrorConnectorProcessingConflict means connector delivery work cannot accept the requested transition.
	ErrorConnectorProcessingConflict ErrorKind = "connector_processing_conflict"
	// ErrorConnectorProcessingLeaseActive means recovery was requested before the exact claim lease elapsed.
	ErrorConnectorProcessingLeaseActive ErrorKind = "connector_processing_lease_active"
	// ErrorConnectorProcessingLeaseExpired means a processing request lost its claim authority.
	ErrorConnectorProcessingLeaseExpired ErrorKind = "connector_processing_lease_expired"
	// ErrorConnectorProcessingFailed means an exact processing request already ended in failure.
	ErrorConnectorProcessingFailed ErrorKind = "connector_processing_failed"
	// ErrorMCPTransportFailed means the selected local MCP process did not complete its bounded read call.
	ErrorMCPTransportFailed ErrorKind = "mcp_transport_failed"
	// ErrorMCPContractViolation means an MCP tool or result contradicted its pinned read contract.
	ErrorMCPContractViolation ErrorKind = "mcp_contract_violation"
	// ErrorMCPProposalConversionRejected means a persisted MCP source cannot
	// safely enter the deterministic proposal conversion surface.
	ErrorMCPProposalConversionRejected ErrorKind = "mcp_proposal_conversion_rejected"
)

// DomainError carries a stable detective error kind.
type DomainError struct {
	Kind    ErrorKind
	Message string
}

func (e *DomainError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Kind)
	}
	return string(e.Kind) + ": " + e.Message
}

func newDomainError(kind ErrorKind, format string, args ...any) error {
	return &DomainError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// KindOf returns the stable detective error kind when err wraps a DomainError.
func KindOf(err error) (ErrorKind, bool) {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Kind, true
	}
	return "", false
}
