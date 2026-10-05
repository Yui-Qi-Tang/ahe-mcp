package logicresolver

import (
	"errors"
	"fmt"
)

// Status is a backend's satisfiability answer, separate from validation.
type Status string

const (
	// Unknown means the backend has not established SAT or UNSAT.
	Unknown Status = "UNKNOWN"
	// SAT means the backend reports a satisfying interpretation.
	SAT Status = "SAT"
	// UNSAT means the backend reports no satisfying interpretation.
	UNSAT Status = "UNSAT"
)

var (
	// ErrInput marks malformed input.
	ErrInput = errors.New("invalid resolver input")
	// ErrUnsupported marks a language construct outside the supported profile.
	ErrUnsupported = errors.New("unsupported logic")
	// ErrLimit marks a configured input, output or accepted proof limit.
	ErrLimit = errors.New("resolver limit exceeded")
	// ErrProtocol marks output inconsistent with the pinned tool protocol.
	ErrProtocol = errors.New("invalid tool response")
	// ErrValidation marks an invalid model or unverified proof.
	ErrValidation = errors.New("resolver validation failed")
	// ErrGuarantee marks an unmet consumer acceptance requirement.
	ErrGuarantee = errors.New("required guarantee unavailable")
)

// Artifact is a relative file identity within the configured artifact root.
type Artifact struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Validation separates availability, decoding and successful checking.
// Decoded applies only to models; proof syntax is handled by the external checker.
// Method and TargetSHA256 are populated only after a successful check.
// For models the target is input.json; for proofs it is the encoded formula.
type Validation struct {
	Present      bool   `json:"present"`
	Decoded      bool   `json:"decoded"`
	Checked      bool   `json:"checked"`
	Method       string `json:"method,omitempty"`
	TargetSHA256 string `json:"target_sha256,omitempty"`
}

// Result binds one answer to the input, encoding, tools and check records.
// Callers must inspect the Solve error before using the result. Even on error,
// a backend answer and partial artifacts may be available for diagnosis.
type Result struct {
	Status        Status     `json:"status"`
	Profile       string     `json:"profile"`
	InputSHA256   string     `json:"input_sha256,omitempty"`
	FormulaSHA256 string     `json:"formula_sha256,omitempty"`
	SolverSHA256  string     `json:"solver_sha256"`
	CheckerSHA256 string     `json:"checker_sha256,omitempty"`
	Model         Validation `json:"model"`
	Proof         Validation `json:"proof"`
	Artifacts     []Artifact `json:"artifacts,omitempty"`
}

// Requirements specifies the checks a consumer needs for each possible answer.
// It is application policy, not a promise that every backend can provide it.
type Requirements struct {
	SATModelChecked   bool
	UNSATProofChecked bool
}

// Require checks answer-specific guarantees after a successful Solve call.
// UNKNOWN is never accepted. It does not authenticate an arbitrary Result.
func (r Result) Require(want Requirements) error {
	switch r.Status {
	case SAT:
		if want.SATModelChecked && (!r.Model.Present || !r.Model.Decoded || !r.Model.Checked || r.Model.TargetSHA256 != r.InputSHA256 || r.InputSHA256 == "") {
			return fmt.Errorf("%w: checked SAT model", ErrGuarantee)
		}
	case UNSAT:
		if want.UNSATProofChecked && (!r.Proof.Present || !r.Proof.Checked || r.Proof.TargetSHA256 != r.FormulaSHA256 || r.FormulaSHA256 == "") {
			return fmt.Errorf("%w: checked UNSAT proof", ErrGuarantee)
		}
	default:
		return fmt.Errorf("%w: no conclusive answer", ErrGuarantee)
	}
	return nil
}
