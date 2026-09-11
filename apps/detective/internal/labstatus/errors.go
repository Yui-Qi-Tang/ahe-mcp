package labstatus

import "errors"

var (
	// ErrModelResponse identifies a model or agent invocation failure.
	ErrModelResponse = errors.New("model response failed")
	// ErrModelJSON identifies missing, oversized, or malformed final JSON.
	ErrModelJSON = errors.New("model JSON failed")
	// ErrCitation identifies a model citation that cannot be grounded.
	ErrCitation = errors.New("model citation failed")
	// ErrCandidateValidation identifies a candidate that fails validation.
	ErrCandidateValidation = errors.New("candidate validation failed")
)
