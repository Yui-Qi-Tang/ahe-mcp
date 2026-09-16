package evidencereferences

import (
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

const (
	// ResolverSameSnapshotV1 resolves a local anchor in one immutable view.
	ResolverSameSnapshotV1 = "same-snapshot-anchor/v1"
	// ResolverNativeSnapshotV1 requires the token itself to pin another snapshot.
	ResolverNativeSnapshotV1 = "native-snapshot-anchor/v1"
	// ResolutionContractV1 identifies the exact resolver output.
	ResolutionContractV1 = "canonical-references-resolution/v1"
	// TargetCutContractV1 identifies observed, not permanent, target uniqueness.
	TargetCutContractV1 = "canonical-references-target-cut/v1"
	// MaxTokenBytes bounds the complete literal reference token.
	MaxTokenBytes = 256
)

// ErrUnresolved denotes a rejected coordinate, source or anchor proof.
var ErrUnresolved = errors.New("references could not resolve exact native evidence")

// ReviewRequest shares the exact retained wire schema with ingestion.
type ReviewRequest = evidenceingestion.CanonicalReferencesReviewRequest

// ReferenceWitness shares the exact retained wire schema with ingestion.
type ReferenceWitness = evidenceingestion.CanonicalReferencesReferenceWitness

// TargetWitness shares the exact retained wire schema with ingestion.
type TargetWitness = evidenceingestion.CanonicalReferencesTargetWitness

// TargetCut shares the exact retained wire schema with ingestion.
type TargetCut = evidenceingestion.CanonicalReferencesTargetCut

// Resolution shares the exact retained wire schema with ingestion.
type Resolution = evidenceingestion.CanonicalReferencesResolution
